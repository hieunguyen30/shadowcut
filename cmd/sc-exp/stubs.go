package main

import "github.com/spf13/cobra"

func exp1Cmd() *cobra.Command      { return &cobra.Command{Use: "exp1"} }
func exp2Cmd() *cobra.Command      { return &cobra.Command{Use: "exp2"} }
func exp4Cmd() *cobra.Command      { return &cobra.Command{Use: "exp4"} }
func exp5Cmd() *cobra.Command      { return &cobra.Command{Use: "exp5"} }
func isolationCmd() *cobra.Command { return &cobra.Command{Use: "isolation"} }
