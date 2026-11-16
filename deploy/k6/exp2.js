// Experiment 2 load: realistic read traffic against the demo app, whose reads go
// through the shadow proxy. Parameters (wikis, prefixes, users, repos) are drawn
// from the live data so queries hit real rows.
import http from 'k6/http';
import { sleep } from 'k6';

const BASE = __ENV.BASE || 'http://demoapp:8080';
const WIKIS = ['enwiki', 'dewiki', 'frwiki', 'commonswiki', 'wikidatawiki', 'eswiki', 'jawiki', 'ruwiki', 'itwiki', 'zhwiki', 'plwiki', 'arwiki'];
const PREFIXES = ['A', 'B', 'C', 'Ma', 'St', 'The', 'Le', 'Die', 'Kat', 'Ö', 'É', '_', '1', 'Ч', '東', 'Q1', 'File:', 'Category:', 'User', 'Wikipedia:'];
const ALPHA = ['A', 'H', 'M', 'S', 'Z', 'a', 'm', '_', 'É', 'Ö', '1', 'Ж', 'あ'];

export const options = {
  vus: Number(__ENV.VUS || 6),
  duration: __ENV.DURATION || '10m',
  summaryTrendStats: ['avg', 'p(50)', 'p(95)', 'max'],
};

function pick(a) { return a[Math.floor(Math.random() * a.length)]; }

export function setup() {
  const users = [];
  for (const w of ['wikidatawiki', 'commonswiki', 'enwiki']) {
    const r = http.get(`${BASE}/api/editors/top?wiki=${w}`);
    if (r.status === 200) for (const row of r.json()) if (row.user_name) users.push(row.user_name);
  }
  const repos = [];
  const g = http.get(`${BASE}/api/gh/repos/top`);
  if (g.status === 200) for (const row of g.json()) repos.push(row.repo_name);
  return { users: users.length ? users : ['Bibliowiki'], repos: repos.length ? repos : ['torvalds/linux'] };
}

const routes = [
  [15, () => `/api/pages/top?wiki=${pick(WIKIS)}&limit=20`],
  [12, () => `/api/feed`],
  [15, () => `/api/search?q=${encodeURIComponent(pick(PREFIXES))}`],
  [10, () => `/api/pages/alpha?wiki=${pick(WIKIS)}&after=${encodeURIComponent(pick(ALPHA))}`],
  [10, (d) => `/api/users/${encodeURIComponent(pick(d.users))}/edits`],
  [4, () => `/api/stats/wikis`],
  [5, () => `/api/stats/daily?wiki=${pick(WIKIS)}`],
  [5, () => `/api/editors/recent`],
  [5, () => `/api/editors/top?wiki=${pick(WIKIS)}`],
  [5, () => `/api/feed/offset?offset=${25 * Math.floor(Math.random() * 8)}`],
  [6, () => {
    const to = new Date(Date.now() - Math.floor(Math.random() * 600) * 1000);
    const from = new Date(to.getTime() - 20000);
    return `/api/edits?from=${from.toISOString()}&to=${to.toISOString()}`;
  }],
  [3, () => `/api/gh/repos/top`],
  [3, (d) => `/api/gh/repos/${pick(d.repos)}`],
];
const total = routes.reduce((s, r) => s + r[0], 0);

export default function (data) {
  let x = Math.random() * total;
  for (const [w, f] of routes) {
    x -= w;
    if (x <= 0) {
      const path = f(data);
      const r = http.get(`${BASE}${path}`, { tags: { route: path.split('?')[0].replace(/\/api\/(users|gh\/repos)\/[^/]+(\/.*)?$/, '/api/$1/:x$2') } });
      // follow the keyset cursor sometimes (feed_page)
      if (path === '/api/feed' && r.status === 200 && Math.random() < 0.5) {
        const rows = r.json();
        if (rows.length) {
          const last = rows[rows.length - 1];
          http.get(`${BASE}/api/feed?before_ts=${encodeURIComponent(new Date(last.edit_ts).toISOString())}&before_id=${last.id}`, { tags: { route: '/api/feed?cursor' } });
        }
      }
      break;
    }
  }
  sleep(0.2 + Math.random() * 0.4);
}
