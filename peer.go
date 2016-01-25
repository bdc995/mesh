package mesh

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log"
	"sort"
	"strconv"
)

// Peer is a local representation of a peer, including connections to other
// peers. By itself, it is a remote peer.
type Peer struct {
	Name PeerName
	PeerSummary
	localRefCount uint64 // maintained by Peers
	connections   map[PeerName]connection
}

// PeerSummary is a collection of identifying information for a peer.
type PeerSummary struct {
	NameByte   []byte
	NickName   string
	UID        PeerUID
	Version    uint64
	ShortID    PeerShortID
	HasShortID bool
}

// ConnectionSet is an set of connection objects.
type connectionSet map[connection]struct{}

// NewPeerFromSummary constructs a new Peer object with no connections from
// the provided summary.
func newPeerFromSummary(summary PeerSummary) *Peer {
	return &Peer{
		Name:        PeerNameFromBin(summary.NameByte),
		PeerSummary: summary,
		connections: make(map[PeerName]connection),
	}
}

// NewPeer constructs a new Peer object with no connections from the provided
// composite parts.
func newPeer(name PeerName, nickName string, uid PeerUID, version uint64, shortID PeerShortID) *Peer {
	return newPeerFromSummary(PeerSummary{
		NameByte:   name.Bin(),
		NickName:   nickName,
		UID:        uid,
		Version:    version,
		ShortID:    shortID,
		HasShortID: true,
	})
}

// NewPeerPlaceholder constructs a partial Peer object with only the passed
// name. Useful when we get a strange update from the mesh.
func newPeerPlaceholder(name PeerName) *Peer {
	return newPeerFromSummary(PeerSummary{NameByte: name.Bin()})
}

// NewPeerFrom constructs a new Peer object that is a copy of the passed peer.
// Primarily used for tests.
func newPeerFrom(peer *Peer) *Peer {
	return newPeerFromSummary(peer.PeerSummary)
}

// String returns the peer name and nickname.
func (peer *Peer) String() string {
	return fmt.Sprint(peer.Name, "(", peer.NickName, ")")
}

// Routes calculates the routing table from this peer to all peers reachable
// from it, returning a "next hop" map of PeerNameX -> PeerNameY, which says
// "in order to send a message to X, the peer should send the message to its
// neighbour Y".
//
// Because currently we do not have weightings on the connections between
// peers, there is no need to use a minimum spanning tree algorithm. Instead
// we employ the simpler and cheaper breadth-first widening. The computation
// is deterministic, which ensures that when it is performed on the same data
// by different peers, they get the same result. This is important since
// otherwise we risk message loss or routing cycles.
//
// When the 'establishedAndSymmetric' flag is set, only connections that are
// marked as 'established' and are symmetric (i.e. where both sides indicate
// they have a connection to the other) are considered.
//
// When a non-nil stopAt peer is supplied, the widening stops when it reaches
// that peer. The boolean return indicates whether that has happened.
//
// NB: This function should generally be invoked while holding a read lock on
// Peers and LocalPeer.
func (peer *Peer) Routes(stopAt *Peer, establishedAndSymmetric bool) (bool, map[PeerName]PeerName) {
	routes := make(unicastRoutes)
	routes[peer.Name] = UnknownPeerName
	nextWorklist := []*Peer{peer}
	for len(nextWorklist) > 0 {
		worklist := nextWorklist
		sort.Sort(listOfPeers(worklist))
		nextWorklist = []*Peer{}
		for _, curPeer := range worklist {
			if curPeer == stopAt {
				return true, routes
			}
			curPeer.ForEachConnectedPeer(establishedAndSymmetric, routes,
				func(remotePeer *Peer) {
					nextWorklist = append(nextWorklist, remotePeer)
					remoteName := remotePeer.Name
					// We now know how to get to remoteName: the same
					// way we get to curPeer. Except, if curPeer is
					// the starting peer in which case we know we can
					// reach remoteName directly.
					if curPeer == peer {
						routes[remoteName] = remoteName
					} else {
						routes[remoteName] = routes[curPeer.Name]
					}
				})
		}
	}
	return false, routes
}

// ForEachConnectedPeer applies f to all peers reachable by peer. If
// establishedAndSymmetric is true, only peers with established bidirectional
// connections will be selected. The exclude maps is treated as a set of
// remote peers to blacklist.
// TODO(pb): change exclude to map[PeerName]struct{}?
func (peer *Peer) ForEachConnectedPeer(establishedAndSymmetric bool, exclude map[PeerName]PeerName, f func(*Peer)) {
	for remoteName, conn := range peer.connections {
		if establishedAndSymmetric && !conn.Established() {
			continue
		}
		if _, found := exclude[remoteName]; found {
			continue
		}
		remotePeer := conn.Remote()
		if remoteConn, found := remotePeer.connections[peer.Name]; !establishedAndSymmetric || (found && remoteConn.Established()) {
			f(remotePeer)
		}
	}
}

// PeerUID uniquely identifies a peer in a mesh.
type PeerUID uint64

// ParsePeerUID parses a decimal peer UID from a string.
func parsePeerUID(s string) (PeerUID, error) {
	uid, err := strconv.ParseUint(s, 10, 64)
	return PeerUID(uid), err
}

func randomPeerUID() PeerUID {
	for {
		uid := randUint64()
		if uid != 0 { // uid 0 is reserved for peer placeholder
			return PeerUID(uid)
		}
	}
}

// PeerShortID exists for the sake of fast datapath. They are 12 bits,
// randomly assigned, but we detect and recover from collisions. This
// does limit us to 4096 peers, but that should be sufficient for a
// while.
// TODO(pb): does this need to be exported?
type PeerShortID uint16

// PeerShortIDBits is the usable bitsize of a PeerShortID.
// TODO(pb): does this need to be exported?
const PeerShortIDBits = 12

func randomPeerShortID() PeerShortID {
	return PeerShortID(randUint16() & (1<<PeerShortIDBits - 1))
}

func randBytes(n int) []byte {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		log.Fatal(err)
	}
	return buf
}

func randUint64() (r uint64) {
	return binary.LittleEndian.Uint64(randBytes(8))
}

func randUint16() (r uint16) {
	return binary.LittleEndian.Uint16(randBytes(2))
}

// ListOfPeers implements sort.Interface on a slice of Peers.
// TODO(pb): does this need to be exported?
type listOfPeers []*Peer

// Len implements sort.Interface.
func (lop listOfPeers) Len() int {
	return len(lop)
}

// Swap implements sort.Interface.
func (lop listOfPeers) Swap(i, j int) {
	lop[i], lop[j] = lop[j], lop[i]
}

// Less implements sort.Interface.
func (lop listOfPeers) Less(i, j int) bool {
	return lop[i].Name < lop[j].Name
}
