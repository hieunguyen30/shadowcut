package main

import "github.com/spf13/cobra"

// Temporary stubs; replaced as each layer lands.
func gateCmd() *cobra.Command     { return &cobra.Command{Use: "gate"} }
func cutoverCmd() *cobra.Command  { return &cobra.Command{Use: "cutover"} }
func rollbackCmd() *cobra.Command { return &cobra.Command{Use: "rollback"} }
func explainCmd() *cobra.Command  { return &cobra.Command{Use: "explain"} }
