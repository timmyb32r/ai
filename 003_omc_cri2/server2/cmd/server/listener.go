package main

import (
	"net"
	"sync"
)

// Bound accepted sockets as well as active HTTP handlers. Idle/slow clients
// otherwise consume descriptors without occupying the handler semaphore.
type boundedListener struct {
	net.Listener
	slots  chan struct{}
	closed chan struct{}
	once   sync.Once
}

func newBoundedListener(l net.Listener, n int) *boundedListener {
	return &boundedListener{Listener: l, slots: make(chan struct{}, n), closed: make(chan struct{})}
}
func (l *boundedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.closed:
		return nil, net.ErrClosed
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &boundedConn{Conn: conn, release: func() { <-l.slots }}, nil
}
func (l *boundedListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

type boundedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *boundedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
