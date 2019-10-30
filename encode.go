package enmime

import (
	"bufio"
	"encoding/base64"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/textproto"
	"sort"
	"time"

	"github.com/jhillyerd/enmime/internal/coding"
	"github.com/jhillyerd/enmime/internal/stringutil"
)

// b64Percent determines the percent of non-ASCII characters enmime will tolerate before switching
// from quoted-printable to base64 encoding.
const b64Percent = 20

type TransferEncoding byte

const (
	TE7Bit TransferEncoding = iota
	TEQuoted
	TEBase64
)

var crnl = []byte{'\r', '\n'}

// HeaderEncoderFactory returns a newly instantiated HeaderEncoder
type HeaderEncoderFactory func(e *Encoder, p *Part) (HeaderEncoder, error)

type Encoder struct {
	transferEncodingDeterminer func([]byte, bool) TransferEncoding
	contentTypeDeterminer      func(*Part) bool
	boundaryGenerator          func() string
	headerEncoderFactory       HeaderEncoderFactory
}

// setupMIMEHeaders determines content transfer encoding, generates a boundary string if required,
// then sets the Content-Type (type, charset, filename, boundary) and Content-Disposition headers.
func (e *Encoder) setupMIMEHeaders(p *Part) TransferEncoding {
	// Determine content transfer encoding.

	// If we are encoding a part that previously had content-transfer-encoding set, unset it so
	// the correct encoding detection can be done below.
	p.Header.Del(hnContentEncoding)

	cte := TE7Bit
	if len(p.Content) > 0 {
		cte = TEBase64
		if e.contentTypeDeterminer(p) {
			cte = e.transferEncodingDeterminer(p.Content, false)
			if p.Charset == "" {
				p.Charset = utf8
			}
		}
		// RFC 2045: 7bit is assumed if CTE header not present.
		switch cte {
		case TEBase64:
			p.Header.Set(hnContentEncoding, cteBase64)
		case TEQuoted:
			p.Header.Set(hnContentEncoding, cteQuotedPrintable)
		}
	}
	// Setup headers.
	if p.FirstChild != nil && p.Boundary == "" {
		// Multipart, generate random boundary marker.
		p.Boundary = e.boundaryGenerator()
	}
	if p.ContentID != "" {
		p.Header.Set(hnContentID, coding.ToIDHeader(p.ContentID))
	}
	if p.ContentType != "" {
		// Build content type header.
		param := make(map[string]string)
		setParamValue(param, hpCharset, p.Charset)
		setParamValue(param, hpName, stringutil.ToASCII(p.FileName))
		setParamValue(param, hpBoundary, p.Boundary)
		mt := mime.FormatMediaType(p.ContentType, param)
		if mt == "" {
			// There was an error, FormatMediaType couldn't encode the params.
			mt = p.ContentType
		}
		p.Header.Set(hnContentType, mt)
	}
	if p.Disposition != "" {
		// Build disposition header.
		param := make(map[string]string)
		setParamValue(param, hpFilename, stringutil.ToASCII(p.FileName))
		if !p.FileModDate.IsZero() {
			setParamValue(param, hpModDate, p.FileModDate.Format(time.RFC822))
		}
		mt := mime.FormatMediaType(p.Disposition, param)
		if mt == "" {
			// There was an error, FormatMediaType couldn't encode the params.
			mt = p.Disposition
		}
		p.Header.Set(hnContentDisposition, mt)
	}
	return cte
}

func (e *Encoder) Encode(p *Part, writer io.Writer) error {
	if p.Header == nil {
		p.Header = make(textproto.MIMEHeader)
	}
	cte := e.setupMIMEHeaders(p)
	// Encode this part.
	b := bufio.NewWriter(writer)
	if err := e.encodeHeader(p, b); err != nil {
		return err
	}
	if len(p.Content) > 0 {
		b.Write(crnl)
		if err := e.encodeContent(p, b, cte); err != nil {
			return err
		}
	}
	if p.FirstChild == nil {
		return b.Flush()
	}
	// Encode children.
	endMarker := []byte("\r\n--" + p.Boundary + "--")
	marker := endMarker[:len(endMarker)-2]
	c := p.FirstChild
	for c != nil {
		b.Write(marker)
		b.Write(crnl)
		if err := c.Encode(b); err != nil {
			return err
		}
		c = c.NextSibling
	}
	b.Write(endMarker)
	b.Write(crnl)
	return b.Flush()
}

// encodeHeader writes out a sorted list of headers.
func (e *Encoder) encodeHeader(p *Part, b *bufio.Writer) error {
	keys := make([]string, 0, len(p.Header))
	for k := range p.Header {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	headerEncoder, err := e.headerEncoderFactory(e, p)
	if err != nil {
		return err
	}
	for _, k := range keys {
		for _, v := range p.Header[k] {
			// TODO: headerEncoder is expected to fold it. Should fix this later.
			_, encv, err := headerEncoder.Encode(0, v)
			if err != nil {
				return err
			}
			// _ used to prevent early wrapping
			wb := stringutil.Wrap(76, k, ":_", encv, "\r\n")
			wb[len(k)+1] = ' '
			if _, err := b.Write(wb); err != nil {
				return err
			}
		}
	}
	return nil
}

// encodeContent writes out the content in the selected encoding.
func (e *Encoder) encodeContent(p *Part, b *bufio.Writer, cte TransferEncoding) (err error) {
	switch cte {
	case TEBase64:
		enc := base64.StdEncoding
		text := make([]byte, enc.EncodedLen(len(p.Content)))
		base64.StdEncoding.Encode(text, p.Content)
		// Wrap lines.
		lineLen := 76
		for len(text) > 0 {
			if lineLen > len(text) {
				lineLen = len(text)
			}
			if _, err = b.Write(text[:lineLen]); err != nil {
				return err
			}
			b.Write(crnl)
			text = text[lineLen:]
		}
	case TEQuoted:
		qp := quotedprintable.NewWriter(b)
		if _, err = qp.Write(p.Content); err != nil {
			return err
		}
		err = qp.Close()
	default:
		_, err = b.Write(p.Content)
	}
	return err
}

// SelectTransferEncoding scans content for non-ASCII characters and selects 'b' or 'q' encoding.
func SelectTransferEncoding(content []byte, quoteLineBreaks bool) TransferEncoding {
	if len(content) == 0 {
		return TE7Bit
	}
	// Binary chars remaining before we choose b64 encoding.
	threshold := b64Percent * len(content) / 100
	bincount := 0
	for _, b := range content {
		if (b < ' ' || '~' < b) && b != '\t' {
			if !quoteLineBreaks && (b == '\r' || b == '\n') {
				continue
			}
			bincount++
			if bincount >= threshold {
				return TEBase64
			}
		}
	}
	if bincount == 0 {
		return TE7Bit
	}
	return TEQuoted
}

// setParamValue will ignore empty values
func setParamValue(p map[string]string, k, v string) {
	if v != "" {
		p[k] = v
	}
}

type EncoderOption func(*Encoder) *Encoder

func WithTransferEncodingDeterminer(det func([]byte, bool) TransferEncoding) EncoderOption {
	return func(e *Encoder) *Encoder {
		e.transferEncodingDeterminer = det
		return e
	}
}

func WithContentTypeDeterminer(det func(*Part) bool) EncoderOption {
	return func(e *Encoder) *Encoder {
		e.contentTypeDeterminer = det
		return e
	}
}

func WithHeaderEncoderFactory(f HeaderEncoderFactory) EncoderOption {
	return func(e *Encoder) *Encoder {
		e.headerEncoderFactory = f
		return e
	}
}

type flexibleHeaderEncoder struct {
	*Encoder
	p *Part
}

func (e *flexibleHeaderEncoder) Encode(startColumn int, v string) (int, string, error) {
	cs := e.p.Charset
	if cs == "" {
		cs = utf8
	}
	switch e.transferEncodingDeterminer([]byte(v), true) {
	case TEBase64:
		v = mime.BEncoding.Encode(cs, v)
	case TEQuoted:
		v = mime.QEncoding.Encode(cs, v)
	default:
	}
	return startColumn + len(v), v, nil
}

func newFlexibleHeaderEncoder(e *Encoder, p *Part) (HeaderEncoder, error) {
	return &flexibleHeaderEncoder{e, p}, nil
}

func NewEncoder(options ...EncoderOption) *Encoder {
	return &Encoder{
		transferEncodingDeterminer: SelectTransferEncoding,
		contentTypeDeterminer:      func(p *Part) bool { return p.TextContent() },
		boundaryGenerator:          func() string { return "enmime-" + stringutil.UUID() },
		headerEncoderFactory:       newFlexibleHeaderEncoder,
	}
}

var DefaultEncoder = NewEncoder()

// Encode writes this Part and all its children to the specified writer in MIME format.
func (p *Part) Encode(writer io.Writer) error {
	return DefaultEncoder.Encode(p, writer)
}
