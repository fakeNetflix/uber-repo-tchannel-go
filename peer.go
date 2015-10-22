// Copyright (c) 2015 Uber Technologies, Inc.

// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in
// all copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
// THE SOFTWARE.

package tchannel

import (
	"errors"
	"sync"
	"time"

	"golang.org/x/net/context"
)

var (
	// ErrInvalidConnectionState indicates that the connection is not in a valid state.
	ErrInvalidConnectionState = errors.New("connection is in an invalid state")

	// ErrNoPeers indicates that there are no peers.
	ErrNoPeers = errors.New("no peers available")

	peerRng = NewRand(time.Now().UnixNano())
)

type Connectable interface {
	Connect(context.Context, string, *ConnectionOptions) (*Connection, error)
	ConnectionOptions() *ConnectionOptions
}

// PeerList maintains a list of Peers.
type PeerList struct {
	channel Connectable
	parent  *PeerList

	mut             sync.RWMutex // mut protects peers.
	peersByHostPort map[string]*Peer
	peers           []*Peer
	peerSelector    *peerSelector
}

func newPeerList(channel Connectable) *PeerList {
	return &PeerList{
		channel:         channel,
		peersByHostPort: make(map[string]*Peer),
		peerSelector:    newPeerSelector(),
	}
}

func (l *PeerList) isRoot() bool {
	return l.parent == nil
}

// Siblings don't share peer lists (though they take care not to double-connect
// to the same hosts).
func (l *PeerList) newSibling() *PeerList {
	sib := newPeerList(l.channel)
	sib.parent = l.parent
	return sib
}

// Children ensure that their parent's peer list is a superset of their own.
func (l *PeerList) newChild() *PeerList {
	child := newPeerList(l.channel)
	child.parent = l
	return child
}

// Add adds a peer to the list if it does not exist, or returns any existing peer.
func (l *PeerList) Add(hostPort string) *Peer {
	l.mut.RLock()

	if p, ok := l.peersByHostPort[hostPort]; ok {
		l.mut.RUnlock()
		return p
	}

	l.mut.RUnlock()
	l.mut.Lock()
	defer l.mut.Unlock()

	if p, ok := l.peersByHostPort[hostPort]; ok {
		return p
	}

	var p *Peer
	if l.isRoot() {
		// To avoid duplicate connections, only the root list should create new
		// peers. All other lists should keep refs to the root list's peers.
		p = newPeer(l.channel, hostPort)
	} else {
		p = l.parent.Add(hostPort)
	}
	l.peersByHostPort[hostPort] = p
	l.peers = append(l.peers, p)
	return p
}

// Get returns a peer from the peer list, or nil if none can be found.
func (l *PeerList) Get() (*Peer, error) {
	l.mut.RLock()

	if len(l.peers) == 0 {
		l.mut.RUnlock()
		return nil, ErrNoPeers
	}

	peer := l.peerSelector.choosePeer(l.peers)
	l.mut.RUnlock()

	return peer, nil
}

// GetOrAdd returns a peer for the given hostPort, creating one if it doesn't yet exist.
func (l *PeerList) GetOrAdd(hostPort string) *Peer {
	l.mut.RLock()
	if p, ok := l.peersByHostPort[hostPort]; ok {
		l.mut.RUnlock()
		return p
	}

	l.mut.RUnlock()
	return l.Add(hostPort)
}

// Copy returns a map of the peer list. This method should only be used for testing.
func (l *PeerList) Copy() map[string]*Peer {
	l.mut.RLock()
	defer l.mut.RUnlock()

	listCopy := make(map[string]*Peer)
	for k, v := range l.peersByHostPort {
		listCopy[k] = v
	}
	return listCopy
}

// Close closes connections for all peers.
func (l *PeerList) Close() {
	l.mut.RLock()
	defer l.mut.RUnlock()

	for _, p := range l.peers {
		p.Close()
	}
}

// Peer represents a single autobahn service or client with a unique host:port.
type Peer struct {
	channel  Connectable
	hostPort string

	mut                 sync.RWMutex // mut protects connections.
	inboundConnections  []*Connection
	outboundConnections []*Connection
}

func newPeer(channel Connectable, hostPort string) *Peer {
	return &Peer{
		channel:  channel,
		hostPort: hostPort,
	}
}

// HostPort returns the host:port used to connect to this peer.
func (p *Peer) HostPort() string {
	return p.hostPort
}

// getActive returns a list of active connections.
// TODO(prashant): Should we clear inactive connections?
func (p *Peer) getActive() []*Connection {
	p.mut.RLock()

	var active []*Connection
	p.runWithConnections(func(c *Connection) {
		if c.IsActive() {
			active = append(active, c)
		}
	})

	p.mut.RUnlock()
	return active
}

func randConn(conns []*Connection) *Connection {
	return conns[peerRng.Intn(len(conns))]
}

// GetConnection returns an active connection to this peer. If no active connections
// are found, it will create a new outbound connection and return it.
func (p *Peer) GetConnection(ctx context.Context) (*Connection, error) {
	// TODO(prashant): Use some sort of scoring to pick a connection.
	if activeConns := p.getActive(); len(activeConns) > 0 {
		return randConn(activeConns), nil
	}

	// No active connections, make a new outgoing connection.
	c, err := p.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// AddInboundConnection adds an active inbound connection to the peer's connection list.
// If a connection is not active, ErrInvalidConnectionState will be returned.
func (p *Peer) AddInboundConnection(c *Connection) error {
	switch c.readState() {
	case connectionActive, connectionStartClose:
		// TODO(prashantv): Block inbound connections when the connection is not active.
		break
	default:
		return ErrInvalidConnectionState
	}

	p.mut.Lock()
	defer p.mut.Unlock()

	p.inboundConnections = append(p.inboundConnections, c)
	return nil
}

// AddOutboundConnection adds an active outbound connection to the peer's connection list.
// If a connection is not active, ErrInvalidConnectionState will be returned.
func (p *Peer) AddOutboundConnection(c *Connection) error {
	switch c.readState() {
	case connectionActive, connectionStartClose:
		break
	default:
		return ErrInvalidConnectionState
	}

	p.mut.Lock()
	defer p.mut.Unlock()

	p.outboundConnections = append(p.outboundConnections, c)
	return nil
}

// Connect adds a new outbound connection to the peer.
func (p *Peer) Connect(ctx context.Context) (*Connection, error) {
	c, err := p.channel.Connect(ctx, p.hostPort, p.channel.ConnectionOptions())
	if err != nil {
		return nil, err
	}

	if err := p.AddOutboundConnection(c); err != nil {
		return nil, err
	}

	return c, nil
}

// BeginCall starts a new call to this specific peer, returning an OutboundCall that can
// be used to write the arguments of the call.
func (p *Peer) BeginCall(ctx context.Context, serviceName string, operationName string, callOptions *CallOptions) (*OutboundCall, error) {
	conn, err := p.GetConnection(ctx)
	if err != nil {
		return nil, err
	}

	if callOptions == nil {
		callOptions = defaultCallOptions
	}
	call, err := conn.beginCall(ctx, serviceName, callOptions, operationName)
	if err != nil {
		return nil, err
	}

	return call, err
}

func (p *Peer) runWithConnections(f func(*Connection)) {
	for _, c := range p.inboundConnections {
		f(c)
	}

	for _, c := range p.outboundConnections {
		f(c)
	}
}

// Close closes all connections to this peer.
func (p *Peer) Close() {
	p.mut.RLock()
	defer p.mut.RUnlock()

	p.runWithConnections(func(c *Connection) {
		c.Close()
	})
}
