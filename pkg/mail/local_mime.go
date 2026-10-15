package mail

import (
	"bufio"
	"encoding/base64"
	"io"
	"mime/quotedprintable"
	"strings"
)

// localMIMETransferReader shares streaming decoders while letting body reads
// tolerate unknown encodings and attachment reads reject them. Stored draft
// reconstruction has a distinct, stricter decoder and does not use this helper.
func localMIMETransferReader(body io.Reader, encoding string) (io.Reader, bool) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, newlineStripper{bufio.NewReader(body)}), true
	case "quoted-printable":
		return quotedprintable.NewReader(body), true
	case "", "7bit", "8bit", "binary":
		return body, true
	default:
		return body, false
	}
}
