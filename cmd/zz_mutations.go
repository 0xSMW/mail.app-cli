package cmd

// Keep this registration alongside the command tree rather than relying on
// command names in prepare. A command that changes state must be marked here
// when it is introduced.
func init() {
	markMutation(
		seenCmd, unseenCmd, flagCmd, unflagCmd, archiveCmd, deleteCmd, moveCmd, sendCmd,
		attachmentsSaveCmd,
		configSetCmd, configUnsetCmd,
		draftsCreateCmd, draftsUpdateCmd, draftsSendCmd, draftsDeleteCmd,
		exportAttachmentsCmd,
		messagesMarkCmd, messagesFlagCmd, messagesDeleteCmd, messagesArchiveCmd, messagesMoveCmd,
		messagesBatchArchiveCmd, messagesBatchDeleteCmd, messagesBatchMoveCmd, messagesBatchMarkCmd, messagesBatchFlagCmd,
		recentClearCmd,
		rulesCreateCmd, rulesEnableCmd, rulesDisableCmd, rulesDeleteCmd,
		skillInstallCmd,
		syncCmd,
		threadsArchiveCmd,
	)
	markFileOutputMutation(exportMessagesCmd)
}
