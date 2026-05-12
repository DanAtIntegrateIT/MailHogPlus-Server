package smtp

// http://www.rfc-editor.org/rfc/rfc5321.txt

import (
	"encoding/base64"
	"io"
	"log"
	"strings"

	"github.com/ian-kent/linkio"
	"github.com/mailhog/MailHog-Server/config"
	"github.com/mailhog/MailHog-Server/monkey"
	"github.com/mailhog/data"
	"github.com/mailhog/smtp"
	"github.com/mailhog/storage"
)

// Session represents a SMTP session using net.TCPConn
type Session struct {
	conn          io.ReadWriteCloser
	proto         *smtp.Protocol
	storage       storage.Storage
	messageChan   chan *data.Message
	remoteAddress string
	isTLS         bool
	line          string
	link          *linkio.Link

	reader io.Reader
	writer io.Writer
	monkey monkey.ChaosMonkey

	config *config.Config
	authenticatedUsername string
}

const folderHeaderName = "X-MailHogPlus-Folder"

// Accept starts a new SMTP session using io.ReadWriteCloser
func Accept(remoteAddress string, conn io.ReadWriteCloser, cfg *config.Config, storage storage.Storage, messageChan chan *data.Message, hostname string, monkey monkey.ChaosMonkey) {
	defer conn.Close()

	proto := smtp.NewProtocol()
	proto.Hostname = hostname
	var link *linkio.Link
	reader := io.Reader(conn)
	writer := io.Writer(conn)
	if monkey != nil {
		linkSpeed := monkey.LinkSpeed()
		if linkSpeed != nil {
			link = linkio.NewLink(*linkSpeed * linkio.BytePerSecond)
			reader = link.NewLinkReader(io.Reader(conn))
			writer = link.NewLinkWriter(io.Writer(conn))
		}
	}

	session := &Session{
		conn:          conn,
		proto:         proto,
		storage:       storage,
		messageChan:   messageChan,
		remoteAddress: remoteAddress,
		isTLS:         false,
		line:          "",
		link:          link,
		reader:        reader,
		writer:        writer,
		monkey:        monkey,
		config:        cfg,
	}
	proto.LogHandler = session.logf
	proto.MessageReceivedHandler = session.acceptMessage
	proto.ValidateSenderHandler = session.validateSender
	proto.ValidateRecipientHandler = session.validateRecipient
	proto.ValidateAuthenticationHandler = session.validateAuthentication
	proto.SMTPVerbFilter = session.smtpVerbFilter
	proto.GetAuthenticationMechanismsHandler = func() []string { return []string{"PLAIN"} }

	session.logf("Starting session")
	session.Write(proto.Start())
	for session.Read() == true {
		if monkey != nil && monkey.Disconnect() {
			session.conn.Close()
			break
		}
	}
	session.logf("Session ended")
}

func (c *Session) validateAuthentication(mechanism string, args ...string) (errorReply *smtp.Reply, ok bool) {
	if c.monkey != nil {
		ok := c.monkey.ValidAUTH(mechanism, args...)
		if !ok {
			// FIXME better error?
			return smtp.ReplyUnrecognisedCommand(), false
		}
	}
	username := extractAuthenticatedUsername(mechanism, args...)
	if c.config != nil && !c.config.IsFolderAllowed(username) {
		return smtp.ReplyInvalidAuth(), false
	}
	if len(username) > 0 {
		c.authenticatedUsername = username
	}
	return nil, true
}

func (c *Session) smtpVerbFilter(verb string, args ...string) (errorReply *smtp.Reply) {
	if c.config == nil || !c.config.ForceDefaultInboxOnly {
		return nil
	}
	if strings.EqualFold(verb, "MAIL") && len(strings.TrimSpace(c.authenticatedUsername)) == 0 {
		return smtp.ReplyInvalidAuth()
	}
	return nil
}

func (c *Session) validateRecipient(to string) bool {
	if c.monkey != nil {
		ok := c.monkey.ValidRCPT(to)
		if !ok {
			return false
		}
	}
	return true
}

func (c *Session) validateSender(from string) bool {
	if c.monkey != nil {
		ok := c.monkey.ValidMAIL(from)
		if !ok {
			return false
		}
	}
	return true
}

func (c *Session) acceptMessage(msg *data.SMTPMessage) (id string, err error) {
	m := msg.Parse(c.proto.Hostname)
	setFolderHeader(m, c.authenticatedUsername)
	persistMessageContentToRaw(m)
	c.logf("Storing message %s", m.ID)
	id, err = c.storage.Store(m)
	c.messageChan <- m
	return
}

func persistMessageContentToRaw(m *data.Message) {
	if m == nil || m.Raw == nil {
		return
	}
	b, err := io.ReadAll(m.Bytes())
	if err != nil {
		return
	}
	m.Raw.Data = string(b)
}

func extractAuthenticatedUsername(mechanism string, args ...string) string {
	switch strings.ToUpper(strings.TrimSpace(mechanism)) {
	case "PLAIN":
		if len(args) > 0 {
			return strings.TrimSpace(args[0])
		}
	case "LOGIN":
		if len(args) > 0 {
			return decodeBase64OrRaw(args[0])
		}
	case "EXTERNAL":
		if len(args) > 0 {
			return decodeBase64OrRaw(args[0])
		}
	}
	return ""
}

func decodeBase64OrRaw(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 {
		return ""
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return strings.TrimSpace(string(decoded))
	}
	return value
}

func setFolderHeader(m *data.Message, folder string) {
	if m == nil || m.Content == nil {
		return
	}
	if m.Content.Headers == nil {
		m.Content.Headers = make(map[string][]string)
	}
	for k := range m.Content.Headers {
		if strings.EqualFold(k, folderHeaderName) {
			delete(m.Content.Headers, k)
		}
	}
	if len(folder) > 0 {
		m.Content.Headers[folderHeaderName] = []string{folder}
	}
}

func (c *Session) logf(message string, args ...interface{}) {
	message = strings.Join([]string{"[SMTP %s]", message}, " ")
	args = append([]interface{}{c.remoteAddress}, args...)
	log.Printf(message, args...)
}

// Read reads from the underlying net.TCPConn
func (c *Session) Read() bool {
	buf := make([]byte, 1024)
	n, err := c.reader.Read(buf)

	if n == 0 {
		c.logf("Connection closed by remote host\n")
		io.Closer(c.conn).Close() // not sure this is necessary?
		return false
	}

	if err != nil {
		c.logf("Error reading from socket: %s\n", err)
		return false
	}

	text := string(buf[0:n])
	logText := strings.Replace(text, "\n", "\\n", -1)
	logText = strings.Replace(logText, "\r", "\\r", -1)
	c.logf("Received %d bytes: '%s'\n", n, logText)

	c.line += text

	for strings.Contains(c.line, "\r\n") {
		line, reply := c.proto.Parse(c.line)
		c.line = line

		if reply != nil {
			c.Write(reply)
			if reply.Status == 221 {
				io.Closer(c.conn).Close()
				return false
			}
		}
	}

	return true
}

// Write writes a reply to the underlying net.TCPConn
func (c *Session) Write(reply *smtp.Reply) {
	lines := reply.Lines()
	for _, l := range lines {
		logText := strings.Replace(l, "\n", "\\n", -1)
		logText = strings.Replace(logText, "\r", "\\r", -1)
		c.logf("Sent %d bytes: '%s'", len(l), logText)
		c.writer.Write([]byte(l))
	}
}
