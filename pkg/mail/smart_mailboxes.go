package mail

import (
	"strconv"
	"time"
)

type SmartMailbox struct {
	Name       string `json:"name"`
	Account    string `json:"account,omitempty"`
	Unread     int    `json:"unreadCount"`
	TotalCount int    `json:"totalCount"`
}

func (c *Client) ListSmartMailboxes() ([]SmartMailbox, error) {
	// Mail does not expose its custom Smart Mailboxes through its public
	// Apple-event dictionary. Today is the one built-in view we can reproduce
	// from the documented local Envelope Index data without UI automation.
	today, err := c.todaySmartMailbox(time.Now(), time.Local)
	if err != nil {
		return nil, err
	}
	return []SmartMailbox{today}, nil
}

func (c *Client) todaySmartMailbox(now time.Time, loc *time.Location) (SmartMailbox, error) {
	start, end := todayBounds(now, loc)
	query := `
select
	count(*) as TotalCount,
	coalesce(sum(case when m.read = 0 then 1 else 0 end), 0) as UnreadCount
from messages m
where m.deleted = 0
	and m.date_last_viewed >= ` + strconv.FormatInt(start.Unix(), 10) + `
	and m.date_last_viewed < ` + strconv.FormatInt(end.Unix(), 10) + `;`

	var rows []struct {
		TotalCount  int `json:"TotalCount"`
		UnreadCount int `json:"UnreadCount"`
	}
	if err := c.runEnvelopeIndexQuery(query, &rows); err != nil {
		if isEnvelopeIndexUnavailable(err) {
			return SmartMailbox{}, &CapabilityError{
				Capability: "Today smart mailbox",
				Status:     CapabilityUnavailable,
				Cause:      err,
			}
		}
		return SmartMailbox{}, err
	}

	result := SmartMailbox{Name: "Today"}
	if len(rows) > 0 {
		result.TotalCount = rows[0].TotalCount
		result.Unread = rows[0].UnreadCount
	}
	return result, nil
}

// todayBounds uses local calendar boundaries. AddDate advances to the next
// local midnight across DST changes instead of assuming every day is 24 hours.
func todayBounds(now time.Time, loc *time.Location) (start, end time.Time) {
	if loc == nil {
		loc = time.Local
	}
	local := now.In(loc)
	start = time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return start, start.AddDate(0, 0, 1)
}
