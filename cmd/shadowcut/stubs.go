package main

import "github.com/spf13/cobra"

// Temporary stubs; replaced as each layer lands.
func scanCmd() *cobra.Command     { return &cobra.Command{Use: "scan"} }
func shadowCmd() *cobra.Command   { return &cobra.Command{Use: "shadow"} }
func gateCmd() *cobra.Command     { return &cobra.Command{Use: "gate"} }
func cutoverCmd() *cobra.Command  { return &cobra.Command{Use: "cutover"} }
func rollbackCmd() *cobra.Command { return &cobra.Command{Use: "rollback"} }
func reportCmd() *cobra.Command   { return &cobra.Command{Use: "report"} }
func explainCmd() *cobra.Command  { return &cobra.Command{Use: "explain"} }
