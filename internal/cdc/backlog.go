package cdc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Backlog is the CDC work not yet applied to Spanner.
type Backlog struct {
	Records int64 // Kafka records not yet applied, across all CDC partitions
	// OldestPending is the Kafka timestamp of the oldest unapplied record (zero
	// when fully caught up). now - OldestPending is a lower bound on how stale
	// Spanner is, measured per partition so that a fast heartbeat partition can't
	// hide a slow data partition (docs/BUGS.md BUG-005).
	OldestPending time.Time
	Partitions    int
	Behind        []string
}

// MeasureBacklog compares Kafka end offsets with the applier's offsets in Spanner
// and reads the timestamp of the first unapplied record of each lagging partition.
func MeasureBacklog(ctx context.Context, brokers []string, topicPrefix string, sp *pgxpool.Pool) (Backlog, error) {
	var b Backlog
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return b, err
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	topics, err := adm.ListTopics(ctx)
	if err != nil {
		return b, err
	}
	var names []string
	for _, t := range topics.Sorted() {
		if strings.HasPrefix(t.Topic, topicPrefix) {
			names = append(names, t.Topic)
		}
	}
	if len(names) == 0 {
		return b, nil
	}
	ends, err := adm.ListEndOffsets(ctx, names...)
	if err != nil {
		return b, err
	}
	applied := map[string]int64{}
	rows, err := sp.Query(ctx, `SELECT topic, kpartition, koffset FROM sc_offsets`)
	if err != nil {
		return b, err
	}
	for rows.Next() {
		var t string
		var p, o int64
		if err := rows.Scan(&t, &p, &o); err != nil {
			rows.Close()
			return b, err
		}
		applied[fmt.Sprintf("%s/%d", t, p)] = o
	}
	rows.Close()
	pending := map[string]map[int32]kgo.Offset{}
	ends.Each(func(o kadm.ListedOffset) {
		b.Partitions++
		if o.Offset <= 0 {
			return
		}
		next := int64(0)
		if a, ok := applied[fmt.Sprintf("%s/%d", o.Topic, o.Partition)]; ok {
			next = a + 1
		}
		if next >= o.Offset {
			return
		}
		b.Records += o.Offset - next
		b.Behind = append(b.Behind, fmt.Sprintf("%s/%d:%d", o.Topic, o.Partition, o.Offset-next))
		if pending[o.Topic] == nil {
			pending[o.Topic] = map[int32]kgo.Offset{}
		}
		pending[o.Topic][o.Partition] = kgo.NewOffset().At(next)
	})
	if len(pending) == 0 {
		return b, nil
	}
	// read the first pending record of each lagging partition
	rc, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumePartitions(pending), kgo.FetchMaxWait(200*time.Millisecond))
	if err != nil {
		return b, err
	}
	defer rc.Close()
	want := 0
	for _, ps := range pending {
		want += len(ps)
	}
	seen := map[string]bool{}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for len(seen) < want {
		fs := rc.PollFetches(rctx)
		if rctx.Err() != nil {
			break
		}
		fs.EachPartition(func(p kgo.FetchTopicPartition) {
			if len(p.Records) == 0 {
				return
			}
			k := fmt.Sprintf("%s/%d", p.Topic, p.Partition)
			if seen[k] {
				return
			}
			seen[k] = true
			ts := p.Records[0].Timestamp
			if b.OldestPending.IsZero() || ts.Before(b.OldestPending) {
				b.OldestPending = ts
			}
		})
	}
	return b, nil
}
