package cmd

// Keep this registration alongside the command tree rather than relying on
// command names in prepare. A command that changes state must be marked here
// when it is introduced.
func (a *application) markMutations() {
	markMutation(
		a.seenCmd, a.unseenCmd, a.flagCmd, a.unflagCmd, a.archiveCmd, a.deleteCmd, a.moveCmd, a.sendCmd,
		a.attachmentsSaveCmd,
		a.configSetCmd, a.configUnsetCmd,
		a.draftsCreateCmd, a.draftsUpdateCmd, a.draftsSendCmd, a.draftsDeleteCmd,
		a.exportAttachmentsCmd,
		a.messagesMarkCmd, a.messagesFlagCmd, a.messagesDeleteCmd, a.messagesArchiveCmd, a.messagesMoveCmd,
		a.messagesBatchArchiveCmd, a.messagesBatchDeleteCmd, a.messagesBatchMoveCmd, a.messagesBatchMarkCmd, a.messagesBatchFlagCmd,
		a.recentClearCmd,
		a.rulesCreateCmd, a.rulesEnableCmd, a.rulesDisableCmd, a.rulesDeleteCmd,
		a.skillInstallCmd,
		a.syncCmd,
		a.threadsArchiveCmd,
	)
	markFileOutputMutation(a.exportMessagesCmd)
}
