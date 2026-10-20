package mail

import (
	"fmt"
	"time"
)

type relocationRef struct {
	index  int
	source bool
}

func verifyMutationStates(client *Client, opts BatchOptions, verified []BatchItem, schedule []time.Duration) {
	groups := make(map[verificationMailboxKey][]int)
	for i, item := range verified {
		if item.Status == "succeeded" {
			key := verificationMailboxKey{item.Account, item.SourceMailbox}
			groups[key] = append(groups[key], i)
		}
	}

	if len(groups) == 0 {
		return
	}
	snapshots := make(map[verificationMailboxKey]map[string]Message)
	snapshotErrors := make(map[verificationMailboxKey]error)
	for attempt, delay := range schedule {
		if attempt > 0 {
			if err := waitForVerificationBackoff(client.Context(), delay); err != nil {
				for key := range groups {
					snapshotErrors[key] = err
				}
				break
			}
		}
		for key, indices := range groups {
			ids := make([]string, 0, len(indices))
			for _, index := range indices {
				ids = append(ids, verified[index].ID)
			}
			snapshots[key], snapshotErrors[key] = client.getMessageEnvelopesForVerification(key.account, key.mailbox, ids)
			// Read/flag changes can finish as soon as their requested state is
			// observed. Delete still needs the full settling window: an old
			// local ID disappearing does not preclude a regenerated copy.
			if snapshotErrors[key] == nil && opts.Action != "delete" {
				pending := make([]int, 0, len(indices))
				for _, index := range indices {
					item := &verified[index]
					message, present := snapshots[key][item.ID]
					matched := present && ((opts.Action == "mark" && message.Read == opts.Read) || (opts.Action == "flag" && message.Flagged == opts.Flagged))
					if matched {
						item.VerifyStatus = "matched"
					} else {
						pending = append(pending, index)
					}
				}
				if len(pending) == 0 {
					delete(groups, key)
				} else {
					groups[key] = pending
				}
			}
		}
		if len(groups) == 0 {
			break
		}
	}
	for key, indices := range groups {
		for _, index := range indices {
			item := &verified[index]
			if err := snapshotErrors[key]; err != nil {
				message, fallbackErr := client.GetMessageStateForVerificationJSON(item.Account, item.SourceMailbox, item.ID)
				if fallbackErr == nil {
					if message != nil {
						snapshots[key] = map[string]Message{item.ID: *message}
					} else {
						snapshots[key] = map[string]Message{}
					}
				} else {
					item.VerifyStatus = "verification-failed"
					if isVerificationTimeout(fallbackErr) {
						item.VerifyStatus = "unknown_after_timeout"
					}
					item.VerifyError = fallbackErr.Error()
					continue
				}
			}
			message, present := snapshots[key][item.ID]
			switch opts.Action {
			case "delete":
				if present {
					item.VerifyStatus = "present-in-source"
					item.VerifyError = fmt.Sprintf("message still present in %s", item.SourceMailbox)
				} else {
					item.VerifyStatus = "absent-from-source"
				}
			case "mark":
				item.VerifyStatus, item.VerifyError = verifyBoolState(present, message.Read, opts.Read, "read")
			case "flag":
				item.VerifyStatus, item.VerifyError = verifyBoolState(present, message.Flagged, opts.Flagged, "flagged")
			}
		}
	}
	return
}

func verifyMutationRelocations(client *Client, opts BatchOptions, verified []BatchItem, schedule []time.Duration) {
	syncErrors := make(map[string]error)
	syncRequests := make(map[string]string)
	successful := 0
	for _, item := range verified {
		if item.Status == "succeeded" {
			successful++
		}
		if !opts.readOnlyVerification && item.Status == "succeeded" && isValidGmailInboxTransition(item) {
			if _, seen := syncErrors[item.Account]; !seen {
				syncRequests[item.Account] = time.Now().Format(time.RFC3339Nano)
				syncErrors[item.Account] = client.SyncAccount(item.Account)
			}
		}
	}
	if successful == 0 {
		return
	}
	sourceRemoval := make([]bool, len(verified))
	retainedSources := make(map[verificationMailboxKey]bool)
	identityGroups := make(map[verificationMailboxKey][]relocationRef)
	for i, item := range verified {
		if item.Status != "succeeded" || syncErrors[item.Account] != nil {
			continue
		}
		policyKey := verificationMailboxKey{item.Account, item.SourceMailbox + "\x00" + item.TargetMailbox}
		retained, known := retainedSources[policyKey]
		if !known {
			retained = client.canRetainRelocationSource(opts.Action, item)
			retainedSources[policyKey] = retained
		}
		sourceRemoval[i] = !retained
		identityGroups[verificationMailboxKey{item.Account, item.SourceMailbox}] = append(identityGroups[verificationMailboxKey{item.Account, item.SourceMailbox}], relocationRef{index: i, source: true})
		identityGroups[verificationMailboxKey{item.Account, item.TargetMailbox}] = append(identityGroups[verificationMailboxKey{item.Account, item.TargetMailbox}], relocationRef{index: i})
	}
	scopes := make([]identityVerificationScope, 0, len(identityGroups))
	for key, refs := range identityGroups {
		identities := make([]StableIdentity, 0, len(refs))
		for _, ref := range refs {
			identities = append(identities, verified[ref.index].Identity)
		}
		scopes = append(scopes, identityVerificationScope{key: key, identities: identities})
	}
	states := make([]RelocationState, len(verified))
	lookupErrors := make([]error, len(verified))
	observationStart := time.Now()
	var observationOffset time.Duration
	for _, delay := range schedule {
		observationOffset += delay
		if err := waitForVerificationObservation(client.Context(), observationStart, observationOffset); err != nil {
			for i := range verified {
				if verified[i].Status == "succeeded" {
					lookupErrors[i] = err
				}
			}
			break
		}
		states = make([]RelocationState, len(verified))
		lookupErrors = make([]error, len(verified))
		for i, item := range verified {
			states[i] = RelocationState{ObservedAt: time.Now().Format(time.RFC3339Nano), Account: item.Account, SourceMailbox: item.SourceMailbox, DestinationMailbox: item.TargetMailbox, SourceRemovalRequired: sourceRemoval[i]}
			states[i].VerificationScope = "mail-local"
			if requestedAt, ok := syncRequests[item.Account]; ok {
				states[i].VerificationScope = "mail-local-after-sync-request"
				states[i].SyncRequestedAt = requestedAt
			}
		}
		snapshot := client.getIdentitySnapshotForVerification(scopes)
		for key, refs := range identityGroups {
			matches, groupErr := snapshot[key].matches, snapshot[key].err
			for _, ref := range refs {
				item := verified[ref.index]
				present := false
				err := groupErr
				found := matches[item.Identity.verificationKey()]
				if err == nil {
					count := len(found)
					switch count {
					case 0:
					case 1:
						present = true
					default:
						if validRFCMessageID(item.Identity.RFCMessageID) != "" {
							// The snapshot already checked each candidate's
							// live header. A later singleton must not erase
							// the ambiguity observed in this snapshot.
							err = fmt.Errorf("stable identity is ambiguous in %s", key.mailbox)
						} else {
							present, err = client.hasMessageIdentityWithWhoseJXA(key.account, key.mailbox, item.Identity)
						}
					}
				} else if !isVerificationTimeout(err) {
					present, err = client.hasMessageIdentityWithWhoseJXA(key.account, key.mailbox, item.Identity)
				}
				if err != nil {
					lookupErrors[ref.index] = err
					continue
				}
				state := &states[ref.index]
				state.ObservedAt = time.Now().Format(time.RFC3339Nano)
				if ref.source {
					state.SourcePresent = present
				} else {
					state.DestinationPresent = present
				}
				if !present {
					continue
				}
				for _, message := range found {
					if ref.source {
						state.SourceIDs = append(state.SourceIDs, message.ID)
					} else {
						state.DestinationIDs = append(state.DestinationIDs, message.ID)
					}
				}
				if !ref.source {
					state.DestinationRead = agreedReadState(found)
				}
			}
		}
	}
	for i := range verified {
		item := &verified[i]
		if item.Status != "succeeded" {
			continue
		}
		observed := states[i]
		item.Observed = &observed
		if syncErr, ok := syncErrors[item.Account]; ok && syncErr != nil {
			item.VerifyStatus, item.VerifyError = classifyGmailSyncError(syncErr)
			continue
		}
		if err := lookupErrors[i]; err != nil {
			item.VerifyStatus = "applied_destination_unverified"
			if isVerificationTimeout(err) {
				item.VerifyStatus = "unknown_after_timeout"
			}
			item.VerifyError = err.Error()
			continue
		}
		switch {
		case observed.SourcePresent && sourceRemoval[i]:
			item.VerifyStatus = "present_in_source"
			item.VerifyError = fmt.Sprintf("message still present in %s", item.SourceMailbox)
		case observed.DestinationPresent && (opts.MarkReadBefore || item.MarkedRead) && (observed.DestinationRead == nil || !*observed.DestinationRead):
			item.VerifyStatus = "destination_read_unverified"
			item.VerifyError = "destination read status is false, mixed, or unknown"
		case observed.DestinationPresent:
			item.VerifyStatus = "confirmed_destination"
		default:
			item.VerifyStatus = "applied_destination_unverified"
			item.VerifyError = fmt.Sprintf("message left %s but was not found in %s", item.SourceMailbox, item.TargetMailbox)
		}
	}
}

// verifySkippedMarks observes the opt-in mark phases with the same parent
// verification budget, without replaying archive or changing application status.
func verifySkippedMarks(client *Client, opts BatchOptions, items []BatchItem, record func(BatchItem) error) error {
	var marks []BatchItem
	var indices []int
	for i, item := range items {
		if item.Status == "skipped" && item.MarkReadOnSkip && item.MarkedRead {
			item.Status = "succeeded"
			marks, indices = append(marks, item), append(indices, i)
		}
	}
	if len(marks) > 0 {
		checks := VerifyMutations(client, BatchOptions{Action: "mark", Read: true, VerifyBudget: opts.VerifyBudget, readOnlyVerification: opts.readOnlyVerification}, marks)
		for i, check := range checks {
			item := &items[indices[i]]
			item.VerifyStatus, item.VerifyError = check.VerifyStatus, check.VerifyError
			if record != nil {
				if err := record(*item); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
