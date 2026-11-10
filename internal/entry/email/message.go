// Package email accepts authenticated inbound-provider envelopes and delivers
// replies through TLS SMTP. Raw email headers are never authentication evidence.
package email

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

const MaxBody = 6 << 20
const MaxAttachmentBytes = 4 << 20
const MaxAttachments = 8

var ErrMessage = errors.New("invalid or oversized inbound email")

type Addresses []string

func (a *Addresses) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		list, err := mail.ParseAddressList(s)
		if err != nil {
			return ErrMessage
		}
		for _, x := range list {
			*a = append(*a, x.Address)
		}
		return nil
	}
	var ss []string
	if json.Unmarshal(b, &ss) != nil {
		return ErrMessage
	}
	*a = ss
	return nil
}

type Attachment struct {
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	Data        []byte `json:"data"`
}
type Envelope struct {
	MessageID             string            `json:"message_id"`
	From                  string            `json:"from"`
	To                    Addresses         `json:"to"`
	Subject               string            `json:"subject"`
	Text                  string            `json:"text"`
	HTML                  string            `json:"html,omitempty"`
	Headers               map[string]string `json:"headers,omitempty"`
	DMARC                 string            `json:"dmarc"`
	AuthenticationResults string            `json:"authentication_results,omitempty"`
	Attachments           []Attachment      `json:"attachments,omitempty"`
	RawMIME               string            `json:"raw_mime,omitempty"`
}

func Header(headers map[string]string, key string) string {
	for k, v := range headers {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

var messageIDPattern = regexp.MustCompile(`^<[^<>\x00-\x20\x7f@]+@[^<>\x00-\x20\x7f@]+>$`)

func ValidMessageID(s string) bool { return len(s) <= 998 && messageIDPattern.MatchString(s) }

func NormalizeAddress(s string) (string, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil || strings.ContainsAny(a.Address, "\r\n") || !strings.Contains(a.Address, "@") {
		return "", ErrMessage
	}
	return strings.ToLower(a.Address), nil
}
func DecodeEnvelope(body []byte) (Envelope, error) {
	if len(body) > MaxBody {
		return Envelope{}, ErrMessage
	}
	var e Envelope
	if json.Unmarshal(body, &e) != nil {
		return e, ErrMessage
	}
	if e.RawMIME != "" {
		p, err := ParseMIME([]byte(e.RawMIME))
		if err != nil {
			return e, err
		}
		p.DMARC = e.DMARC
		p.AuthenticationResults = e.AuthenticationResults
		e = p
	}
	seenHeaders := map[string]bool{}
	for k, v := range e.Headers {
		key := strings.ToLower(k)
		if seenHeaders[key] || strings.ContainsAny(k, "\r\n:") || strings.ContainsAny(v, "\r\n") {
			return e, ErrMessage
		}
		seenHeaders[key] = true
	}
	if e.MessageID == "" {
		e.MessageID = Header(e.Headers, "Message-ID")
	}
	if !ValidMessageID(e.MessageID) || len(e.Subject) > 4096 || len(e.Text) > 1<<20 || len(e.HTML) > 1<<20 {
		return e, ErrMessage
	}
	from, err := NormalizeAddress(e.From)
	if err != nil {
		return e, err
	}
	e.From = from
	if len(e.To) == 0 || len(e.To) > 20 {
		return e, ErrMessage
	}
	for i, a := range e.To {
		e.To[i], err = NormalizeAddress(a)
		if err != nil {
			return e, err
		}
	}
	if len(e.Attachments) > MaxAttachments {
		return e, ErrMessage
	}
	total := 0
	for i, a := range e.Attachments {
		total += len(a.Data)
		if total > MaxAttachmentBytes || a.Name == "" || strings.ContainsAny(a.Name, "\r\n\x00") {
			return e, ErrMessage
		}
		e.Attachments[i].Name = path.Base(strings.ReplaceAll(a.Name, "\\", "/"))
		if len(e.Attachments[i].Name) > 255 {
			return e, ErrMessage
		}
	}
	// Only trusted envelope DMARC is consumed. Authentication-Results inside MIME
	// can be forged by a sender and is retained purely as inert provider metadata.
	e.RawMIME = ""
	return e, nil
}
func IsAutomatic(e Envelope) bool {
	value := strings.TrimSpace(Header(e.Headers, "Auto-Submitted"))
	if value != "" && !strings.EqualFold(value, "no") {
		return true
	}
	return Header(e.Headers, "X-Auto-Response-Suppress") != "" || strings.EqualFold(Header(e.Headers, "Precedence"), "bulk") || strings.EqualFold(Header(e.Headers, "Precedence"), "list")
}
func ParseMIME(raw []byte) (Envelope, error) {
	if len(raw) > MaxBody {
		return Envelope{}, ErrMessage
	}
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Envelope{}, ErrMessage
	}
	e := Envelope{MessageID: m.Header.Get("Message-ID"), From: m.Header.Get("From"), Headers: map[string]string{}}
	for k, vs := range m.Header {
		if len(vs) > 0 {
			e.Headers[k] = vs[0]
		}
	}
	e.Subject, err = new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		return e, ErrMessage
	}
	to, err := mail.ParseAddressList(m.Header.Get("To"))
	if err != nil {
		return e, ErrMessage
	}
	for _, a := range to {
		e.To = append(e.To, a.Address)
	}
	if err = parsePart(textproto.MIMEHeader(m.Header), m.Body, &e, 0); err != nil {
		return e, err
	}
	return e, nil
}
func parsePart(h textproto.MIMEHeader, r io.Reader, e *Envelope, depth int) error {
	if depth > 8 {
		return ErrMessage
	}
	ct := h.Get("Content-Type")
	if ct == "" {
		ct = "text/plain"
	}
	typ, params, err := mime.ParseMediaType(ct)
	if err != nil {
		return ErrMessage
	}
	if strings.HasPrefix(typ, "multipart/") {
		if params["boundary"] == "" {
			return ErrMessage
		}
		mr := multipart.NewReader(r, params["boundary"])
		for count := 0; ; count++ {
			if count > 64 {
				return ErrMessage
			}
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return ErrMessage
			}
			err = parsePart(p.Header, p, e, depth+1)
			p.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	switch strings.ToLower(h.Get("Content-Transfer-Encoding")) {
	case "base64":
		r = base64.NewDecoder(base64.StdEncoding, r)
	case "quoted-printable":
		r = quotedprintable.NewReader(r)
	}
	data, err := io.ReadAll(io.LimitReader(r, MaxAttachmentBytes+1))
	if err != nil || len(data) > MaxAttachmentBytes {
		return ErrMessage
	}
	disp, dp, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	name := dp["filename"]
	if name == "" {
		name = params["name"]
	}
	if disp == "attachment" || name != "" {
		if len(e.Attachments) >= MaxAttachments {
			return ErrMessage
		}
		if name == "" {
			name = "attachment"
		}
		total := len(data)
		for _, a := range e.Attachments {
			total += len(a.Data)
		}
		if total > MaxAttachmentBytes {
			return ErrMessage
		}
		e.Attachments = append(e.Attachments, Attachment{Name: name, ContentType: typ, Data: data})
		return nil
	}
	if typ != "text/plain" && typ != "text/html" {
		return nil
	}
	charset := strings.ToLower(params["charset"])
	if charset != "" && charset != "utf-8" && charset != "us-ascii" {
		return ErrMessage
	}
	if !utf8.Valid(data) {
		return ErrMessage
	}
	if typ == "text/plain" {
		e.Text += string(data)
	} else {
		e.HTML += string(data)
	}
	if len(e.Text) > 1<<20 || len(e.HTML) > 1<<20 {
		return ErrMessage
	}
	return nil
}
