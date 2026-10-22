package sshclient

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestX11ForwardingRewritesCookieAndRelaysData(t *testing.T) {
	transportListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for test SSH transport: %v", err)
	}
	clientRaw, err := net.Dial("tcp", transportListener.Addr().String())
	if err != nil {
		_ = transportListener.Close()
		t.Fatalf("dial test SSH transport: %v", err)
	}
	serverRaw, err := transportListener.Accept()
	_ = transportListener.Close()
	if err != nil {
		_ = clientRaw.Close()
		t.Fatalf("accept test SSH transport: %v", err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate SSH server key: %v", err)
	}
	serverSigner, err := ssh.NewSignerFromKey(serverKey)
	if err != nil {
		t.Fatalf("create SSH server signer: %v", err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(serverSigner)
	serverReady := make(chan struct {
		conn     *ssh.ServerConn
		channels <-chan ssh.NewChannel
		err      error
	}, 1)
	go func() {
		conn, channels, requests, handshakeErr := ssh.NewServerConn(serverRaw, serverConfig)
		if handshakeErr != nil {
			serverReady <- struct {
				conn     *ssh.ServerConn
				channels <-chan ssh.NewChannel
				err      error
			}{err: handshakeErr}
			return
		}
		go ssh.DiscardRequests(requests)
		serverReady <- struct {
			conn     *ssh.ServerConn
			channels <-chan ssh.NewChannel
			err      error
		}{conn: conn, channels: channels}
	}()
	clientConn, clientChannels, clientRequests, err := ssh.NewClientConn(clientRaw, "x11-test", &ssh.ClientConfig{
		User: "tester", HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("create SSH client connection: %v", err)
	}
	client := ssh.NewClient(clientConn, clientChannels, clientRequests)
	t.Cleanup(func() { _ = client.Close() })
	server := <-serverReady
	if server.err != nil {
		t.Fatalf("create SSH server connection: %v", server.err)
	}
	t.Cleanup(func() { _ = server.conn.Close() })
	requestPayload := make(chan []byte, 1)
	go func() {
		for newChannel := range server.channels {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "only session channels are used by this test")
				continue
			}
			channel, requests, acceptErr := newChannel.Accept()
			if acceptErr != nil {
				continue
			}
			go func() {
				defer channel.Close()
				for request := range requests {
					if request.Type == "x11-req" {
						requestPayload <- append([]byte(nil), request.Payload...)
						_ = request.Reply(true, nil)
						continue
					}
					_ = request.Reply(true, nil)
				}
			}()
		}
	}()

	session, err := client.NewSession()
	if err != nil {
		t.Fatalf("create SSH session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen as fake local X server: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	realCookie := []byte("real-local-x11-cookie")
	fakeCookie := []byte("temporary-fake-cookie")
	config := x11Config{
		display:    x11Display{network: "tcp", address: listener.Addr().String(), screen: 2},
		realCookie: realCookie,
		fakeCookie: fakeCookie,
	}
	forwarder, err := startX11Forwarding(client, session, config)
	if err != nil {
		t.Fatalf("start X11 forwarding: %v", err)
	}
	t.Cleanup(forwarder.Close)
	select {
	case payload := <-requestPayload:
		var request x11Request
		if err := ssh.Unmarshal(payload, &request); err != nil {
			t.Fatalf("decode X11 request: %v", err)
		}
		if request.AuthProtocol != x11AuthProtocol || request.AuthCookie != hex.EncodeToString(fakeCookie) || request.Screen != 2 {
			t.Fatalf("X11 request = %#v; auth protocol, fake cookie, or screen is incorrect", request)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("SSH server did not receive x11-req")
	}

	localResult := make(chan error, 1)
	go func() {
		local, acceptErr := listener.Accept()
		if acceptErr != nil {
			localResult <- acceptErr
			return
		}
		defer local.Close()
		_ = local.SetDeadline(time.Now().Add(3 * time.Second))
		header := make([]byte, 12)
		if _, acceptErr = io.ReadFull(local, header); acceptErr != nil {
			localResult <- acceptErr
			return
		}
		var order binary.ByteOrder
		if header[0] == 'l' {
			order = binary.LittleEndian
		} else if header[0] == 'B' {
			order = binary.BigEndian
		} else {
			localResult <- io.ErrUnexpectedEOF
			return
		}
		nameLength := int(order.Uint16(header[6:8]))
		dataLength := int(order.Uint16(header[8:10]))
		name := make([]byte, paddedX11Length(nameLength))
		data := make([]byte, paddedX11Length(dataLength))
		if _, acceptErr = io.ReadFull(local, name); acceptErr == nil {
			_, acceptErr = io.ReadFull(local, data)
		}
		if acceptErr != nil {
			localResult <- acceptErr
			return
		}
		if string(name[:nameLength]) != x11AuthProtocol || dataLength != len(realCookie) || !equalX11Bytes(data[:dataLength], realCookie) {
			localResult <- io.ErrUnexpectedEOF
			return
		}
		tail := make([]byte, len("remote-x11-data"))
		if _, acceptErr = io.ReadFull(local, tail); acceptErr != nil {
			localResult <- acceptErr
			return
		}
		if string(tail) != "remote-x11-data" {
			localResult <- io.ErrUnexpectedEOF
			return
		}
		_, acceptErr = local.Write([]byte("local-x11-reply"))
		localResult <- acceptErr
	}()

	channel, requests, err := server.conn.OpenChannel("x11", nil)
	if err != nil {
		t.Fatalf("open forwarded X11 channel: %v", err)
	}
	go ssh.DiscardRequests(requests)
	t.Cleanup(func() { _ = channel.Close() })
	channelTimeout := time.AfterFunc(3*time.Second, func() { _ = channel.Close() })
	defer channelTimeout.Stop()
	setup := makeX11Setup(binary.LittleEndian, x11AuthProtocol, fakeCookie, []byte("remote-x11-data"))
	if _, err := channel.Write(setup); err != nil {
		t.Fatalf("write remote X11 setup: %v", err)
	}
	reply := make([]byte, len("local-x11-reply"))
	if _, err := io.ReadFull(channel, reply); err != nil {
		t.Fatalf("read forwarded local X11 reply: %v", err)
	}
	if string(reply) != "local-x11-reply" {
		t.Fatalf("X11 reply = %q, want local-x11-reply", reply)
	}
	select {
	case err := <-localResult:
		if err != nil {
			t.Fatalf("local X server forwarding: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("local X server forwarding did not finish")
	}
}

func equalX11Bytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
