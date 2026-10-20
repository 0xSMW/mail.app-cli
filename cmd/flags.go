package cmd

import "github.com/spf13/cobra"

func registerPagingFlags(cmd *cobra.Command, limit, offset *int) {
	cmd.Flags().IntVarP(limit, "limit", "l", 25, "Maximum messages to return")
	cmd.Flags().IntVarP(offset, "offset", "o", 0, "Messages to skip (pagination)")
}

func registerCacheFlags(cmd *cobra.Command, noCache, forceRefresh *bool) {
	cmd.Flags().BoolVar(noCache, "no-cache", false, "Bypass the cache and read from Mail.app")
	cmd.Flags().BoolVar(forceRefresh, "force-refresh", false, "Refresh the cache from Mail.app")
}
