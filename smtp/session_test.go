package smtp

import (
	"encoding/base64"
	"errors"
	"sync"
	"testing"

	"github.com/mailhog/MailHog-Server/config"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/mailhog/data"
	mhsmtp "github.com/mailhog/smtp"
	"github.com/mailhog/storage"
)

type fakeRw struct {
	_read  func(p []byte) (n int, err error)
	_write func(p []byte) (n int, err error)
	_close func() error
}

func (rw *fakeRw) Read(p []byte) (n int, err error) {
	if rw._read != nil {
		return rw._read(p)
	}
	return 0, nil
}
func (rw *fakeRw) Close() error {
	if rw._close != nil {
		return rw._close()
	}
	return nil
}
func (rw *fakeRw) Write(p []byte) (n int, err error) {
	if rw._write != nil {
		return rw._write(p)
	}
	return len(p), nil
}

func TestAccept(t *testing.T) {
	Convey("Accept should handle a connection", t, func() {
		frw := &fakeRw{}
		mChan := make(chan *data.Message)
		Accept("1.1.1.1:11111", frw, nil, storage.CreateInMemory(), mChan, "localhost", nil)
	})
}

func TestSocketError(t *testing.T) {
	Convey("Socket errors should return from Accept", t, func() {
		frw := &fakeRw{
			_read: func(p []byte) (n int, err error) {
				return -1, errors.New("OINK")
			},
		}
		mChan := make(chan *data.Message)
		Accept("1.1.1.1:11111", frw, nil, storage.CreateInMemory(), mChan, "localhost", nil)
	})
}

func TestAcceptMessage(t *testing.T) {
	Convey("acceptMessage should be called", t, func() {
		mbuf := "EHLO localhost\r\nMAIL FROM:<test>\r\nRCPT TO:<test>\r\nDATA\r\nHi.\r\n.\r\nQUIT\r\n"
		var rbuf []byte
		frw := &fakeRw{
			_read: func(p []byte) (n int, err error) {
				if len(p) >= len(mbuf) {
					ba := []byte(mbuf)
					mbuf = ""
					for i, b := range ba {
						p[i] = b
					}
					return len(ba), nil
				}

				ba := []byte(mbuf[0:len(p)])
				mbuf = mbuf[len(p):]
				for i, b := range ba {
					p[i] = b
				}
				return len(ba), nil
			},
			_write: func(p []byte) (n int, err error) {
				rbuf = append(rbuf, p...)
				return len(p), nil
			},
			_close: func() error {
				return nil
			},
		}
		mChan := make(chan *data.Message)
		var wg sync.WaitGroup
		wg.Add(1)
		handlerCalled := false
		go func() {
			handlerCalled = true
			<-mChan
			//FIXME breaks some tests (in drone.io)
			//m := <-mChan
			//So(m, ShouldNotBeNil)
			wg.Done()
		}()
		Accept("1.1.1.1:11111", frw, nil, storage.CreateInMemory(), mChan, "localhost", nil)
		wg.Wait()
		So(handlerCalled, ShouldBeTrue)
	})
}

func TestValidateAuthentication(t *testing.T) {
	Convey("validateAuthentication is always successful", t, func() {
		c := &Session{}

		err, ok := c.validateAuthentication("OINK")
		So(err, ShouldBeNil)
		So(ok, ShouldBeTrue)

		err, ok = c.validateAuthentication("OINK", "arg1")
		So(err, ShouldBeNil)
		So(ok, ShouldBeTrue)

		err, ok = c.validateAuthentication("OINK", "arg1", "arg2")
		So(err, ShouldBeNil)
		So(ok, ShouldBeTrue)
	})

	Convey("validateAuthentication stores authenticated username when present", t, func() {
		c := &Session{}

		err, ok := c.validateAuthentication("PLAIN", "gateway", "secret")
		So(err, ShouldBeNil)
		So(ok, ShouldBeTrue)
		So(c.authenticatedUsername, ShouldEqual, "gateway")
	})

	Convey("validateAuthentication decodes LOGIN username", t, func() {
		c := &Session{}
		encoded := base64.StdEncoding.EncodeToString([]byte("thorlux"))

		err, ok := c.validateAuthentication("LOGIN", encoded, "ignored")
		So(err, ShouldBeNil)
		So(ok, ShouldBeTrue)
		So(c.authenticatedUsername, ShouldEqual, "thorlux")
	})

	Convey("validateAuthentication enforces non-empty allowed usernames when default inbox only is enabled", t, func() {
		cfg := config.DefaultConfig()
		cfg.ForceDefaultInboxOnly = true
		cfg.DefaultFolders = []string{"Gateway", "Operations"}
		c := &Session{config: cfg}

		err, ok := c.validateAuthentication("PLAIN", "", "secret")
		So(err, ShouldNotBeNil)
		So(ok, ShouldBeFalse)
		So(err.Status, ShouldEqual, 535)

		err, ok = c.validateAuthentication("PLAIN", "UnknownFolder", "secret")
		So(err, ShouldNotBeNil)
		So(ok, ShouldBeFalse)
		So(err.Status, ShouldEqual, 535)

		err, ok = c.validateAuthentication("PLAIN", "gateway", "secret")
		So(err, ShouldBeNil)
		So(ok, ShouldBeTrue)
		So(c.authenticatedUsername, ShouldEqual, "gateway")

		err, ok = c.validateAuthentication("PLAIN", "gateway:jon", "secret")
		So(err, ShouldBeNil)
		So(ok, ShouldBeTrue)
		So(c.authenticatedUsername, ShouldEqual, "gateway:jon")

		err, ok = c.validateAuthentication("PLAIN", "gateway:jon:legacy", "secret")
		So(err, ShouldBeNil)
		So(ok, ShouldBeTrue)
		So(c.authenticatedUsername, ShouldEqual, "gateway:jon:legacy")
	})
}

func TestSplitAuthenticatedUsername(t *testing.T) {
	Convey("splitAuthenticatedUsername splits folder and optional tags", t, func() {
		folder, tags := splitAuthenticatedUsername("gateway2lease:jon")
		So(folder, ShouldEqual, "gateway2lease")
		So(tags, ShouldResemble, []string{"jon"})

		folder, tags = splitAuthenticatedUsername("gateway")
		So(folder, ShouldEqual, "gateway")
		So(tags, ShouldResemble, []string{})

		folder, tags = splitAuthenticatedUsername(" gateway : jon : legacy : jon ")
		So(folder, ShouldEqual, "gateway")
		So(tags, ShouldResemble, []string{"jon", "legacy"})

		folder, tags = splitAuthenticatedUsername("gateway2lease%3Ajon%3Alegacy")
		So(folder, ShouldEqual, "gateway2lease")
		So(tags, ShouldResemble, []string{"jon", "legacy"})
	})
}

func TestSMTPVerbFilter(t *testing.T) {
	Convey("smtpVerbFilter rejects MAIL before successful auth when default inbox only is enabled", t, func() {
		cfg := config.DefaultConfig()
		cfg.ForceDefaultInboxOnly = true
		c := &Session{config: cfg}

		reply := c.smtpVerbFilter("MAIL")
		So(reply, ShouldNotBeNil)
		So(reply.Status, ShouldEqual, 535)

		reply = c.smtpVerbFilter("EHLO")
		So(reply, ShouldBeNil)

		c.authenticatedUsername = "Gateway"
		reply = c.smtpVerbFilter("MAIL")
		So(reply, ShouldBeNil)
	})
}

func TestValidateRecipient(t *testing.T) {
	Convey("validateRecipient is always successful", t, func() {
		c := &Session{}

		So(c.validateRecipient("OINK"), ShouldBeTrue)
		So(c.validateRecipient("foo@bar.mailhog"), ShouldBeTrue)
	})
}

func TestValidateSender(t *testing.T) {
	Convey("validateSender is always successful", t, func() {
		c := &Session{}

		So(c.validateSender("OINK"), ShouldBeTrue)
		So(c.validateSender("foo@bar.mailhog"), ShouldBeTrue)
	})
}

func TestAcceptMessageFolderHeader(t *testing.T) {
	Convey("acceptMessage stores folder header when authenticated username is present", t, func() {
		mem := storage.CreateInMemory()
		ch := make(chan *data.Message, 1)
		c := &Session{
			proto:                 &mhsmtp.Protocol{Hostname: "localhost"},
			storage:               mem,
			messageChan:           ch,
			authenticatedUsername: "malachi",
		}
		msg := &data.SMTPMessage{
			From: "sender@example.com",
			To:   []string{"rcpt@example.com"},
			Helo: "client.example.com",
			Data: "Subject: test\r\n\r\nbody",
		}

		id, err := c.acceptMessage(msg)
		So(err, ShouldBeNil)
		So(id, ShouldNotBeEmpty)

		stored, err := mem.Load(id)
		So(err, ShouldBeNil)
		So(stored, ShouldNotBeNil)
		So(stored.Content.Headers[folderHeaderName], ShouldResemble, []string{"malachi"})
	})

	Convey("acceptMessage stores folder and tag headers when authenticated username has tags", t, func() {
		mem := storage.CreateInMemory()
		ch := make(chan *data.Message, 1)
		c := &Session{
			proto:                 &mhsmtp.Protocol{Hostname: "localhost"},
			storage:               mem,
			messageChan:           ch,
			authenticatedUsername: "gateway2lease:jon:legacy",
		}
		msg := &data.SMTPMessage{
			From: "sender@example.com",
			To:   []string{"rcpt@example.com"},
			Helo: "client.example.com",
			Data: "Subject: test\r\n\r\nbody",
		}

		id, err := c.acceptMessage(msg)
		So(err, ShouldBeNil)
		So(id, ShouldNotBeEmpty)

		stored, err := mem.Load(id)
		So(err, ShouldBeNil)
		So(stored, ShouldNotBeNil)
		So(stored.Content.Headers[folderHeaderName], ShouldResemble, []string{"gateway2lease"})
		So(stored.Content.Headers[tagHeaderName], ShouldResemble, []string{"jon:legacy"})
	})

	Convey("acceptMessage strips spoofed folder header when no authenticated username is present", t, func() {
		mem := storage.CreateInMemory()
		ch := make(chan *data.Message, 1)
		c := &Session{
			proto:       &mhsmtp.Protocol{Hostname: "localhost"},
			storage:     mem,
			messageChan: ch,
		}
		msg := &data.SMTPMessage{
			From: "sender@example.com",
			To:   []string{"rcpt@example.com"},
			Helo: "client.example.com",
			Data: "Subject: test\r\nX-MailHogPlus-Folder: spoofed\r\n\r\nbody",
		}

		id, err := c.acceptMessage(msg)
		So(err, ShouldBeNil)
		So(id, ShouldNotBeEmpty)

		stored, err := mem.Load(id)
		So(err, ShouldBeNil)
		So(stored, ShouldNotBeNil)

		_, exists := stored.Content.Headers[folderHeaderName]
		So(exists, ShouldBeFalse)
	})

	Convey("acceptMessage keeps tag headers from message content", t, func() {
		mem := storage.CreateInMemory()
		ch := make(chan *data.Message, 1)
		c := &Session{
			proto:       &mhsmtp.Protocol{Hostname: "localhost"},
			storage:     mem,
			messageChan: ch,
		}
		msg := &data.SMTPMessage{
			From: "sender@example.com",
			To:   []string{"rcpt@example.com"},
			Helo: "client.example.com",
			Data: "Subject: test\r\nX-MailHogPlus-Tags: finance:legacy\r\n\r\nbody",
		}

		id, err := c.acceptMessage(msg)
		So(err, ShouldBeNil)
		So(id, ShouldNotBeEmpty)

		stored, err := mem.Load(id)
		So(err, ShouldBeNil)
		So(stored, ShouldNotBeNil)

		So(stored.Content.Headers[tagHeaderName], ShouldResemble, []string{"finance:legacy"})
		_, exists := stored.Content.Headers[legacyTagHeaderName]
		So(exists, ShouldBeFalse)
	})

	Convey("acceptMessage canonicalizes legacy singular tag header name", t, func() {
		mem := storage.CreateInMemory()
		ch := make(chan *data.Message, 1)
		c := &Session{
			proto:       &mhsmtp.Protocol{Hostname: "localhost"},
			storage:     mem,
			messageChan: ch,
		}
		msg := &data.SMTPMessage{
			From: "sender@example.com",
			To:   []string{"rcpt@example.com"},
			Helo: "client.example.com",
			Data: "Subject: test\r\nX-MailHogPlus-Tag: ops\r\n\r\nbody",
		}

		id, err := c.acceptMessage(msg)
		So(err, ShouldBeNil)
		So(id, ShouldNotBeEmpty)

		stored, err := mem.Load(id)
		So(err, ShouldBeNil)
		So(stored, ShouldNotBeNil)

		So(stored.Content.Headers[tagHeaderName], ShouldResemble, []string{"ops"})
		_, exists := stored.Content.Headers[legacyTagHeaderName]
		So(exists, ShouldBeFalse)
	})

	Convey("acceptMessage appends authenticated username tags to header tags", t, func() {
		mem := storage.CreateInMemory()
		ch := make(chan *data.Message, 1)
		c := &Session{
			proto:                 &mhsmtp.Protocol{Hostname: "localhost"},
			storage:               mem,
			messageChan:           ch,
			authenticatedUsername: "gateway2lease:jon:legacy",
		}
		msg := &data.SMTPMessage{
			From: "sender@example.com",
			To:   []string{"rcpt@example.com"},
			Helo: "client.example.com",
			Data: "Subject: test\r\nX-MailHogPlus-Tags: finance:legacy\r\n\r\nbody",
		}

		id, err := c.acceptMessage(msg)
		So(err, ShouldBeNil)
		So(id, ShouldNotBeEmpty)

		stored, err := mem.Load(id)
		So(err, ShouldBeNil)
		So(stored, ShouldNotBeNil)
		So(stored.Content.Headers[folderHeaderName], ShouldResemble, []string{"gateway2lease"})
		So(stored.Content.Headers[tagHeaderName], ShouldResemble, []string{"finance:legacy:jon"})
		_, exists := stored.Content.Headers[legacyTagHeaderName]
		So(exists, ShouldBeFalse)
	})
}
