// Package osc provides a package for sending and receiving OpenSoundControl
// messages. The package is implemented in pure Go.
package osc

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"reflect"
	"regexp"
	"strings"
	"time"
)

const (
	secondsFrom1900To1970 = 2208988800
	bundleTagString       = "#bundle"
)

// Packet is the interface for Message and Bundle.
type Packet interface {
	MarshalBinary() (data []byte, err error)
}

// Message represents a single OSC message. An OSC message consists of an OSC
// address pattern and zero or more arguments.
type Message struct {
	Address   string
	Arguments []interface{}
}

// Verify that Messages implements the Packet interface.
// var _ Packet = (*Message)(nil)

// Bundle represents an OSC bundle. It consists of the OSC-string "#bundle"
// followed by an OSC Time Tag, followed by zero or more OSC bundle/message
// elements. The OSC-timetag is a 64-bit fixed point time tag. See
// http://opensoundcontrol.org/spec-1_0 for more information.
type Bundle struct {
	Timetag  Timetag
	Messages []*Message
	Bundles  []*Bundle
}

// Verify that Bundle implements the Packet interface.
// var _ Packet = (*Bundle)(nil)

// Client enables you to send OSC packets. It sends OSC messages and bundles to
// the given IP address and port.
type Client struct {
	IP    string
	Port  int
	laddr *net.UDPAddr
}

// Server represents an OSC server. The server listens on Address and Port for
// incoming OSC packets and bundles.
type Server struct {
	Addr        string
	Dispatcher  Dispatcher
	ReadTimeout time.Duration
	close       func() error
}

// Timetag represents an OSC Time Tag.
// An OSC Time Tag is defined as follows:
// Time tags are represented by a 64 bit fixed point number. The first 32 bits
// specify the number of seconds since midnight on January 1, 1900, and the
// last 32 bits specify fractional parts of a second to a precision of about
// 200 picoseconds. This is the representation used by Internet NTP timestamps.
type Timetag struct {
	timeTag uint64 // The acutal time tag
	time    time.Time
}

// Dispatcher is an interface for an OSC message dispatcher. A dispatcher is
// responsible for dispatching received OSC messages.
type Dispatcher interface {
	Dispatch(packet Packet)
}

// Handler is an interface for message handlers. Every handler implementation
// for an OSC message must implement this interface.
type Handler interface {
	HandleMessage(msg *Message)
}

// HandlerFunc implements the Handler interface. Type definition for an OSC
// handler function.
type HandlerFunc func(msg *Message)

// HandleMessage calls itself with the given OSC Message. Implements the
// Handler interface.
func (f HandlerFunc) HandleMessage(msg *Message) {
	f(msg)
}

////
// StandardDispatcher
////

// StandardDispatcher is a dispatcher for OSC packets. It handles the dispatching of
// received OSC packets to Handlers for their given address.
type StandardDispatcher struct {
	handlers       map[string]Handler
	defaultHandler Handler
}

// NewStandardDispatcher returns an StandardDispatcher.
func NewStandardDispatcher() *StandardDispatcher {
	return &StandardDispatcher{
		handlers:       make(map[string]Handler),
		defaultHandler: nil,
	}
}

// AddMsgHandler adds a new message handler for the given OSC address.
func (s *StandardDispatcher) AddMsgHandler(addr string, handler HandlerFunc) error {
	if addr == "*" {
		s.defaultHandler = handler
		return nil
	}

	for _, chr := range "*?,[]{}# " {
		if strings.Contains(addr, fmt.Sprintf("%c", chr)) {
			return ERROR_OSC_INVALID_CHARACTER
		}
	}

	if addressExists(addr, s.handlers) {
		return ERROR_OSC_ADDRESS_EXISTS
	}

	s.handlers[addr] = handler

	return nil
}

// Dispatch dispatches OSC packets. Implements the Dispatcher interface.
func (s *StandardDispatcher) Dispatch(packet Packet) {
	switch p := packet.(type) {
	case *Message:
		for addr, handler := range s.handlers {
			if p.Match(addr) {
				handler.HandleMessage(p)
			}
		}

		if s.defaultHandler != nil {
			s.defaultHandler.HandleMessage(p)
		}

	case *Bundle:
		timer := time.NewTimer(p.Timetag.ExpiresIn())

		go func() {
			<-timer.C

			for _, message := range p.Messages {
				for address, handler := range s.handlers {
					if message.Match(address) {
						handler.HandleMessage(message)
					}
				}

				if s.defaultHandler != nil {
					s.defaultHandler.HandleMessage(message)
				}
			}

			// Process all bundles
			for _, b := range p.Bundles {
				s.Dispatch(b)
			}
		}()
	}
}

////
// Message
////

// NewMessage returns a new Message. The address parameter is the OSC address.
func NewMessage(addr string, args ...interface{}) *Message {
	return &Message{Address: addr, Arguments: args}
}

// Append appends the given arguments to the arguments list.
func (msg *Message) Append(args ...interface{}) {
	msg.Arguments = append(msg.Arguments, args...)
}

// Equals returns true if the given OSC Message `m` is equal to the current OSC
// Message. It checks if the OSC address and the arguments are equal. Returns
// true if the current object and `m` are equal.
func (msg *Message) Equals(m *Message) bool {
	return reflect.DeepEqual(msg, m)
}

// Clear clears the OSC address and all arguments.
func (msg *Message) Clear() {
	msg.Address = ""
	msg.ClearData()
}

// ClearData removes all arguments from the OSC Message.
func (msg *Message) ClearData() {
	msg.Arguments = msg.Arguments[len(msg.Arguments):]
}

// Match returns true, if the OSC address pattern of the OSC Message matches the given
// address. The match is case sensitive!
func (msg *Message) Match(addr string) bool {
	return getRegEx(msg.Address).MatchString(addr)
}

// typeTags returns the type tag string.
func (msg *Message) typeTags() string {
	if len(msg.Arguments) == 0 {
		return ","
	}

	var tags strings.Builder
	_ = tags.WriteByte(',')

	for _, m := range msg.Arguments {
		tags.WriteByte(getTypeTag(m))
	}

	return tags.String()
}

// String implements the fmt.Stringer interface.
func (msg *Message) String() string {
	if msg == nil {
		return ""
	}

	var s strings.Builder
	tags := msg.typeTags()
	s.WriteString(fmt.Sprintf("%s %s", msg.Address, tags))

	for _, arg := range msg.Arguments {
		switch argType := arg.(type) {
		case bool, int32, int64, float32, float64, string:
			s.WriteString(fmt.Sprintf(" %v", argType))

		case nil:
			s.WriteString(" Nil")

		case []byte:
			s.WriteString(fmt.Sprintf(" %s", argType))

		case Timetag:

			s.WriteString(fmt.Sprintf(" %d", argType.TimeTag()))
		}
	}

	return s.String()
}

// MarshalBinary serializes the OSC message to a byte buffer. The byte buffer
// has the following format:
// 1. OSC Address Pattern
// 2. OSC Type Tag String
// 3. OSC Arguments.
func (msg *Message) MarshalBinary() ([]byte, error) {
	// We can start with the OSC address and add it to the buffer
	data := new(bytes.Buffer)

	_, err := writePaddedString(msg.Address, data)
	if err != nil {
		return nil, err
	}

	// Type tag string starts with ","
	lenArgs := len(msg.Arguments)
	typetags := make([]byte, lenArgs+1)
	typetags[0] = ','

	// Process the type tags and collect all arguments
	payload := new(bytes.Buffer)

	for i, arg := range msg.Arguments {
		switch t := arg.(type) {
		case bool:
			if t {
				typetags[i+1] = 'T'
				continue
			}

			typetags[i+1] = 'F'

		case nil:
			typetags[i+1] = 'N'

		case int32:
			typetags[i+1] = 'i'

			err = binary.Write(payload, binary.BigEndian, t)
			if err != nil {
				return nil, err
			}

		case float32:
			typetags[i+1] = 'f'

			err := binary.Write(payload, binary.BigEndian, t)
			if err != nil {
				return nil, err
			}

		case string:
			typetags[i+1] = 's'

			_, err = writePaddedString(t, payload)
			if err != nil {
				return nil, err
			}

		case []byte:
			typetags[i+1] = 'b'

			_, err = writeBlob(t, payload)
			if err != nil {
				return nil, err
			}

		case int64:
			typetags[i+1] = 'h'

			err = binary.Write(payload, binary.BigEndian, t)
			if err != nil {
				return nil, err
			}

		case float64:
			typetags[i+1] = 'd'

			err = binary.Write(payload, binary.BigEndian, t)
			if err != nil {
				return nil, err
			}

		case Timetag:
			typetags[i+1] = 't'

			b, err := t.MarshalBinary()
			if err != nil {
				return nil, err
			}

			_, err = payload.Write(b)
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported type: %T", t)
		}
	}

	// Write the type tag string to the data buffer
	if _, err := writePaddedString(string(typetags), data); err != nil {
		return nil, err
	}

	// Write the payload (OSC arguments) to the data buffer
	if _, err := data.Write(payload.Bytes()); err != nil {
		return nil, err
	}

	return data.Bytes(), nil
}

////
// Bundle
////

// NewBundle returns an OSC Bundle. Use this function to create a new OSC
// Bundle.
func NewBundle(time time.Time) *Bundle {
	return &Bundle{
		Timetag:  *NewTimetag(time),
		Messages: []*Message{},
		Bundles:  []*Bundle{},
	}
}

// Append appends an OSC bundle or OSC message to the bundle.
func (b *Bundle) Append(pck Packet) error {
	switch t := pck.(type) {
	case *Bundle:
		b.Bundles = append(b.Bundles, t)

	case *Message:
		b.Messages = append(b.Messages, t)

	default:
		return ERROR_UNSUPORTED_PACKAGE
	}

	return nil
}

// MarshalBinary serializes the OSC bundle to a byte array with the following
// format:
// 1. Bundle string: '#bundle'
// 2. OSC timetag
// 3. Length of first OSC bundle element
// 4. First bundle element
// 5. Length of n OSC bundle element
// 6. n bundle element.
func (b *Bundle) MarshalBinary() ([]byte, error) {
	// Add the '#bundle' string
	data := new(bytes.Buffer)

	_, err := writePaddedString("#bundle", data)
	if err != nil {
		return nil, err
	}

	// Add the time tag
	bd, err := b.Timetag.MarshalBinary()
	if err != nil {
		return nil, err
	}

	_, err = data.Write(bd)
	if err != nil {
		return nil, err
	}

	// Process all OSC Messages
	for _, m := range b.Messages {
		buf, err := m.MarshalBinary()
		if err != nil {
			return nil, err
		}

		// Append the length of the OSC message
		err = binary.Write(data, binary.BigEndian, int32(len(buf)))
		if err != nil {
			return nil, err
		}

		// Append the OSC message
		_, err = data.Write(buf)
		if err != nil {
			return nil, err
		}
	}

	// Process all OSC Bundles
	for _, b := range b.Bundles {
		buf, err := b.MarshalBinary()
		if err != nil {
			return nil, err
		}

		// Write the size of the bundle
		err = binary.Write(data, binary.BigEndian, int32(len(buf)))
		if err != nil {
			return nil, err
		}

		// Append the bundle
		_, err = data.Write(buf)
		if err != nil {
			return nil, err
		}
	}

	return data.Bytes(), nil
}

////
// Client
////

// NewClient creates a new OSC client. The Client is used to send OSC
// messages and OSC bundles over an UDP network connection. The `ip` argument
// specifies the IP address and `port` defines the target port where the
// messages and bundles will be send to.
func NewClient(ip string, port int) *Client {
	return &Client{
		IP:    ip,
		Port:  port,
		laddr: nil,
	}
}

// SetLocalAddr sets the local address.
func (c *Client) SetLocalAddr(ip string, port int) error {
	laddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", ip, port))
	if err != nil {
		return err
	}

	c.laddr = laddr

	return nil
}

// Send sends an OSC Bundle or an OSC Message.
func (c *Client) Send(packet Packet) error {
	addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", c.IP, c.Port))
	if err != nil {
		return err
	}

	conn, err := net.DialUDP("udp", c.laddr, addr)
	if err != nil {
		return err
	}

	defer conn.Close()

	data, err := packet.MarshalBinary()
	if err != nil {
		return err
	}

	_, err = conn.Write(data)

	return err
}

////
// Server
////

// ListenAndServe retrieves incoming OSC packets and dispatches the retrieved
// OSC packets.
func (s *Server) ListenAndServe() error {
	defer s.Close()

	if s.Dispatcher == nil {
		s.Dispatcher = NewStandardDispatcher()
	}

	ln, err := net.ListenPacket("udp", s.Addr)
	if err != nil {
		return err
	}

	s.close = ln.Close

	return s.Serve(ln)
}

// Serve retrieves incoming OSC packets from the given connection and dispatches
// retrieved OSC packets. If something goes wrong an error is returned.
func (s *Server) Serve(c net.PacketConn) error {
	tempDelay := 25 + time.Millisecond

	for {
		msg, err := s.Read(c)
		if err != nil {
			ne, ok := err.(net.Error)

			if ok && ne.Temporary() {
				time.Sleep(tempDelay)
				continue
			}

			return err
		}

		go s.Dispatcher.Dispatch(msg)
	}
}

// Close forcibly closes a server's connection.
//
// This causes a "use of closed network connection" error the next time the
// server attempts to read from the connection.
func (s *Server) Close() error {
	if s.close == nil {
		return nil
	}

	return s.close()
}

// Read retrieves OSC packets.
func (s *Server) Read(c net.PacketConn) (Packet, error) {
	if s.ReadTimeout != 0 {
		err := c.SetReadDeadline(time.Now().Add(s.ReadTimeout))
		if err != nil {
			return nil, err
		}
	}

	data := make([]byte, 65535)

	n, _, err := c.ReadFrom(data)
	if err != nil {
		return nil, err
	}

	var start int
	p, err := readPacket(bufio.NewReader(bytes.NewBuffer(data)), &start, n)

	return p, err
}

// receivePacket receives an OSC packet from the given reader.
func readPacket(reader *bufio.Reader, start *int, end int) (Packet, error) {
	// var buf []byte
	buf, err := reader.Peek(1)
	if err != nil {
		return nil, err
	}

	switch buf[0] {
	case '/':
		return readMessage(reader, start)

	case '#':
		return readBundle(reader, start, end)
	}

	return nil, ERROR_INVALID_PACKET
}

// readBundle reads an Bundle from reader.
func readBundle(reader *bufio.Reader, start *int, end int) (*Bundle, error) {
	// Read the '#bundle' OSC string
	startTag, n, err := readPaddedString(reader)
	if err != nil {
		return nil, err
	}
	*start += n

	if startTag != bundleTagString {
		return nil, fmt.Errorf("Invalid bundle start tag: %s", startTag)
	}

	// Read the timetag
	var timeTag uint64
	err = binary.Read(reader, binary.BigEndian, &timeTag)
	if err != nil {
		return nil, err
	}

	*start += 8

	// Create a new bundle
	bundle := NewBundle(timetagToTime(timeTag))

	// Read until the end of the buffer
	for *start < end {
		// Read the size of the bundle element
		var length int32

		err = binary.Read(reader, binary.BigEndian, &length)
		if err != nil {
			return nil, err
		}

		*start += 4

		p, err := readPacket(reader, start, end)
		if err != nil {
			return nil, err
		}

		err = bundle.Append(p)
		if err != nil {
			return nil, err
		}
	}

	return bundle, nil
}

// readMessage from `reader`.
func readMessage(reader *bufio.Reader, start *int) (*Message, error) {
	// First, read the OSC address
	addr, n, err := readPaddedString(reader)
	if err != nil {
		return nil, err
	}
	*start += n

	// Read all arguments
	msg := NewMessage(addr)

	err = readArguments(msg, reader, start)
	if err != nil {
		return nil, err
	}

	return msg, nil
}

// readArguments from `reader` and add them to the OSC message `msg`.
func readArguments(msg *Message, reader *bufio.Reader, start *int) error {
	// Read the type tag string
	var n int
	typetags, n, err := readPaddedString(reader)
	if err != nil {
		return err
	}
	*start += n

	if len(typetags) == 0 {
		return nil
	}

	// If the typetag doesn't start with ',', it's not valid
	if typetags[0] != ',' {
		return fmt.Errorf("unsupported type tag string %s", typetags)
	}

	// Remove ',' from the type tag
	typetags = typetags[1:]

	for _, c := range typetags {
		switch c {
		case 'i': // int32
			var i int32

			err = binary.Read(reader, binary.BigEndian, &i)
			if err != nil {
				return err
			}

			*start += 4
			msg.Append(i)

		case 'h': // int64
			var i int64
			err = binary.Read(reader, binary.BigEndian, &i)
			if err != nil {
				return err
			}
			*start += 8
			msg.Append(i)

		case 'f': // float32
			var f float32
			err = binary.Read(reader, binary.BigEndian, &f)
			if err != nil {
				return err
			}
			*start += 4
			msg.Append(f)

		case 'd': // float64/double
			var d float64
			err = binary.Read(reader, binary.BigEndian, &d)
			if err != nil {
				return err
			}
			*start += 8
			msg.Append(d)

		case 's': // string
			// TODO: fix reading string value
			var s string
			s, _, err = readPaddedString(reader)
			if err != nil {
				return err
			}
			lenStr := len(s)
			*start += lenStr + padBytesNeeded(lenStr)
			msg.Append(s)

		case 'b': // blob
			var buf []byte
			var n int
			buf, n, err = readBlob(reader)
			if err != nil {
				return err
			}
			*start += n
			msg.Append(buf)

		case 't': // OSC time tag
			var tt uint64

			err = binary.Read(reader, binary.BigEndian, &tt)
			if err != nil {
				return nil
			}

			*start += 8
			msg.Append(*NewTimetagFromTimetag(tt))

		case 'N': // nil
			msg.Append(nil)

		case 'T': // true
			msg.Append(true)

		case 'F': // false
			msg.Append(false)

		default:
			return fmt.Errorf("unsupported type tag: %c", c)
		}
	}

	return nil
}

////
// Timetag
////

// NewTimetag returns a new OSC time tag object.
func NewTimetag(timeStamp time.Time) *Timetag {
	return &Timetag{
		time:    timeStamp,
		timeTag: timeToTimetag(timeStamp),
	}
}

// NewTimetagFromTimetag creates a new Timetag from the given `timetag`.
func NewTimetagFromTimetag(timetag uint64) *Timetag {
	time := timetagToTime(timetag)
	return NewTimetag(time)
}

// Time returns the time.
func (t *Timetag) Time() time.Time {
	return t.time
}

// FractionalSecond returns the last 32 bits of the OSC time tag. Specifies the
// fractional part of a second.
func (t *Timetag) FractionalSecond() uint32 {
	return uint32(t.timeTag << 32)
}

// SecondsSinceEpoch returns the first 32 bits (the number of seconds since the
// midnight 1900) from the OSC time tag.
func (t *Timetag) SecondsSinceEpoch() uint32 {
	return uint32(t.timeTag >> 32)
}

// TimeTag returns the time tag value.
func (t *Timetag) TimeTag() uint64 {
	return t.timeTag
}

// MarshalBinary converts the OSC time tag to a byte array.
func (t *Timetag) MarshalBinary() ([]byte, error) {
	data := new(bytes.Buffer)
	err := binary.Write(data, binary.BigEndian, t.timeTag)
	return data.Bytes(), err
}

// SetTime sets the value of the OSC time tag.
func (t *Timetag) SetTime(time time.Time) {
	t.time = time
	t.timeTag = timeToTimetag(time)
}

// ExpiresIn calculates the number of seconds until the current time is the
// same as the value of the time tag. It returns zero if the value of the
// time tag is in the past.
func (t *Timetag) ExpiresIn() time.Duration {
	if t.timeTag <= 1 {
		return 0
	}

	tt := timetagToTime(t.timeTag)
	seconds := time.Until(tt)

	if seconds <= 0 {
		return 0
	}

	return seconds
}

// timeToTimetag converts the given time to an OSC time tag.
//
// An OSC time tag is defined as follows:
// Time tags are represented by a 64 bit fixed point number. The first 32 bits
// specify the number of seconds since midnight on January 1, 1900, and the
// last 32 bits specify fractional parts of a second to a precision of about
// 200 picoseconds. This is the representation used by Internet NTP timestamps.
//
// The time tag value consisting of 63 zero bits followed by a one in the least
// significant bit is a special case meaning "immediately.".
func timeToTimetag(time time.Time) (timetag uint64) {
	timetag = uint64((secondsFrom1900To1970 + time.Unix()) << 32)
	return (timetag + uint64(uint32(time.Nanosecond())))
}

// timetagToTime converts the given timetag to a time object.
func timetagToTime(timetag uint64) (t time.Time) {
	return time.Unix(int64((timetag>>32)-secondsFrom1900To1970), int64(timetag&0xffffffff))
}

////
// De/Encoding functions
////

// readBlob reads an OSC blob from the blob byte array. Padding bytes are
// removed from the reader and not returned.
func readBlob(reader *bufio.Reader) ([]byte, int, error) {
	// First, get the length
	var blobLen int32
	if err := binary.Read(reader, binary.BigEndian, &blobLen); err != nil {
		return nil, 0, err
	}
	n := 4 + int(blobLen)

	if blobLen < 1 || blobLen > int32(reader.Buffered()) {
		return nil, 0, fmt.Errorf("readBlob: invalid blob length %d", blobLen)
	}

	// Read the data
	blob := make([]byte, blobLen)
	if _, err := reader.Read(blob); err != nil {
		return nil, 0, err
	}

	// Remove the padding bytes
	numPadBytes := padBytesNeeded(int(blobLen))
	if numPadBytes > 0 {
		n += numPadBytes
		dummy := make([]byte, numPadBytes)
		if _, err := reader.Read(dummy); err != nil {
			return nil, 0, err
		}
	}

	return blob, n, nil
}

// writeBlob writes the data byte array as an OSC blob into buff. If the length
// of data isn't 32-bit aligned, padding bytes will be added.
func writeBlob(data []byte, buf *bytes.Buffer) (int, error) {
	// Add the size of the blob
	lenData := len(data)
	err := binary.Write(buf, binary.BigEndian, int32(lenData))
	if err != nil {
		return 0, err
	}

	// Write the data
	_, err = buf.Write(data)
	if err != nil {
		return 0, err
	}

	// Add padding bytes if necessary
	numPadBytes := padBytesNeeded(lenData)
	if numPadBytes > 0 {
		padBytes := make([]byte, numPadBytes)
		n, err := buf.Write(padBytes)
		if err != nil {
			return 0, err
		}
		numPadBytes = n
	}

	return 4 + lenData + numPadBytes, nil
}

// readPaddedString reads a padded string from the given reader. The padding
// bytes are removed from the reader.
func readPaddedString(reader *bufio.Reader) (string, int, error) {
	// Read the string from the reader
	str, err := reader.ReadString(0)
	if err != nil {
		return "", 0, err
	}
	lenStr := len(str)
	n := lenStr

	// Remove the padding bytes (leaving the null delimiter)
	padLen := padBytesNeeded(lenStr)
	if padLen > 0 {
		n += padLen
		padBytes := make([]byte, padLen)
		if _, err = reader.Read(padBytes); err != nil {
			return "", 0, err
		}
	}

	// Strip off the string delimiter
	return str[:lenStr-1], n, nil
}

// writePaddedString writes a string with padding bytes to the a buffer.
// Returns, the number of written bytes and an error if any.
func writePaddedString(str string, buf *bytes.Buffer) (int, error) {
	// Truncate at the first null, just in case there is more than one present
	nullIndex := strings.Index(str, "\x00")
	if nullIndex > 0 {
		str = str[:nullIndex]
	}
	// Write the string to the buffer
	n, err := buf.WriteString(str)
	if err != nil {
		return 0, err
	}

	// Always write a null terminator, as we stripped it earlier if it existed
	buf.WriteByte(0)
	n++

	// Calculate the padding bytes needed and create a buffer for the padding bytes
	numPadBytes := padBytesNeeded(n)
	if numPadBytes > 0 {
		padBytes := make([]byte, numPadBytes)
		// Add the padding bytes to the buffer
		n, err := buf.Write(padBytes)
		if err != nil {
			return 0, err
		}
		numPadBytes = n
	}

	return n + numPadBytes, nil
}

// padBytesNeeded determines how many bytes are needed to fill up to the next 4
// byte length.
func padBytesNeeded(elementLen int) int {
	return ((4 - (elementLen % 4)) % 4)
}

////
// Utility and helper functions
////

// addressExists returns true if the OSC address `addr` is found in `handlers`.
func addressExists(addr string, handlers map[string]Handler) bool {
	for h := range handlers {
		if h == addr {
			return true
		}
	}
	return false
}

// getRegEx compiles and returns a regular expression object for the given
// address `pattern`.
func getRegEx(pattern string) *regexp.Regexp {
	for _, trs := range []struct {
		old, new string
	}{
		{".", `\.`}, // Escape all '.' in the pattern
		{"(", `\(`}, // Escape all '(' in the pattern
		{")", `\)`}, // Escape all ')' in the pattern
		{"*", ".*"}, // Replace a '*' with '.*' that matches zero or more chars
		{"{", "("},  // Change a '{' to '('
		{",", "|"},  // Change a ',' to '|'
		{"}", ")"},  // Change a '}' to ')'
		{"?", "."},  // Change a '?' to '.'
	} {
		pattern = strings.Replace(pattern, trs.old, trs.new, -1)
	}

	return regexp.MustCompile(pattern)
}

// getTypeTag returns the OSC type tag for the given argument.
func getTypeTag(arg interface{}) byte {
	switch t := arg.(type) {
	case bool:
		if t {
			return 'T'
		}
		return 'F'
	case nil:
		return 'N'
	case int32:
		return 'i'
	case float32:
		return 'f'
	case string:
		return 's'
	case []byte:
		return 'b'
	case int64:
		return 'h'
	case float64:
		return 'd'
	case Timetag:
		return 't'
	default:
		return '\xff'
	}
}
