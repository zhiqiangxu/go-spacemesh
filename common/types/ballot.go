package types

import (
	"bytes"
	"fmt"

	"github.com/spacemeshos/ed25519"

	"github.com/spacemeshos/go-spacemesh/codec"
	"github.com/spacemeshos/go-spacemesh/log"
	"github.com/spacemeshos/go-spacemesh/signing"
)

const (
	// BallotIDSize in bytes.
	// FIXME(dshulyak) why do we cast to hash32 when returning bytes?
	BallotIDSize = Hash32Length
)

// BallotID is a 20-byte sha256 sum of the serialized ballot used to identify a Ballot.
type BallotID Hash20

// EmptyBallotID is a canonical empty BallotID.
var EmptyBallotID = BallotID{}

// Ballot contains the smesher's signed vote on the mesh history.
type Ballot struct {
	// the actual votes on the mesh history
	InnerBallot
	// smesher's signature on InnerBallot
	Signature []byte

	// the following fields are kept private and from being serialized
	ballotID BallotID
	// the public key of the smesher used
	smesherID *signing.PublicKey
}

// InnerBallot contains all info about a smesher's votes on the mesh history. this structure is
// serialized and signed to produce the signature in Ballot.
type InnerBallot struct {
	// the smesher's ATX in the epoch this ballot is cast.
	AtxID ATXID
	// the proof of the smesher's eligibility to vote and propose block content in this epoch.
	EligibilityProof VotingEligibilityProof

	// a smesher creates votes in the following steps:
	// - select a Ballot in the past as a base Ballot
	// - calculate the opinion difference on history between the smesher and the base Ballot
	// - encode the opinion difference in 3 list:
	//	 - ForDiff
	//	   contains blocks we support while the base ballot did not support (i.e. voted against)
	//	   for blocks we support in layers later than the base ballot, we also add them to this list
	//   - AgainstDiff
	//     contains blocks we vote against while the base ballot explicitly supported
	//	 - NeutralDiff
	//	   contains blocks we vote neutral while the base ballot explicitly supported or voted against
	//
	// example:
	// layer | unified content block
	// -----------------------------------------------------------------------------------------------
	//   N   | UCB_A (genesis)
	// -----------------------------------------------------------------------------------------------
	//  N+1  | UCB_B base:UCB_A, for:[UCB_A], against:[], neutral:[]
	// -----------------------------------------------------------------------------------------------
	//  N+2  | UCB_C base:UCB_B, for:[UCB_B], against:[], neutral:[]
	// -----------------------------------------------------------------------------------------------
	//  (hare hasn't terminated for N+2)
	//  N+3  | UCB_D base:UCB_B, for:[UCB_B], against:[], neutral:[UCB_C]
	// -----------------------------------------------------------------------------------------------
	//  (hare succeeded for N+2 but failed for N+3)
	//  N+4  | UCB_E base:UCB_C, for:[UCB_C], against:[], neutral:[]
	// -----------------------------------------------------------------------------------------------
	// NOTE on neutral votes: a base block is by default neutral on all blocks and layers that come after it, so
	// there's no need to explicitly add neutral votes for more recent layers.
	// TODO: optimize this data structure in two ways:
	//   - neutral votes are only ever for an entire layer, never for a subset of blocks.
	//   - collapse AgainstDiff and ForDiff into a single list.
	//   see https://github.com/spacemeshos/go-spacemesh/issues/2369.
	BaseBallot  BallotID
	AgainstDiff []BlockID
	ForDiff     []BlockID
	NeutralDiff []BlockID

	// the first Ballot the smesher cast in the epoch. this Ballot is a special Ballot that contains information
	// that cannot be changed mid-epoch.
	RefBallot BallotID
	EpochData *EpochData

	// the layer ID in which this ballot is eligible for. this will be validated via EligibilityProof
	LayerIndex LayerID
}

// EpochData contains information that cannot be changed mid-epoch.
type EpochData struct {
	// from the smesher's view, the set of ATXs eligible to vote and propose block content in this epoch
	ActiveSet []ATXID
	// the beacon value the smesher recorded for this epoch
	Beacon Beacon
}

// VotingEligibilityProof includes the required values that, along with the smesher's VRF public key,
// allow non-interactive voting eligibility validation. this proof provides eligibility for both voting and
// making proposals.
type VotingEligibilityProof struct {
	// the counter value used to generate this eligibility proof. if the value of J is 3, this is the smesher's
	// eligibility proof of the 3rd ballot/proposal in the epoch.
	J uint32
	// the VRF signature of some epoch specific data and J. one can derive a Ballot's layerID from this signature.
	Sig []byte
}

// Initialize calculates and sets the Ballot's cached ballotID and smesherID.
// this should be called once all the other fields of the Ballot are set.
func (b *Ballot) Initialize() error {
	if b.ID() != EmptyBallotID {
		return fmt.Errorf("ballot already initialized")
	}

	data := b.Bytes()
	b.ballotID = BallotID(CalcHash32(data).ToHash20())
	pubkey, err := ed25519.ExtractPublicKey(data, b.Signature)
	if err != nil {
		return fmt.Errorf("ballot extract key: %w", err)
	}
	b.smesherID = signing.NewPublicKey(pubkey)
	return nil
}

// Bytes returns the serialization of the InnerBallot.
func (b *Ballot) Bytes() []byte {
	data, err := codec.Encode(b.InnerBallot)
	if err != nil {
		log.Panic("failed to serialize ballot: %v", err)
	}
	return data
}

// ID returns the BallotID.
func (b *Ballot) ID() BallotID {
	return b.ballotID
}

// SmesherID returns the smesher's Edwards public key.
func (b *Ballot) SmesherID() *signing.PublicKey {
	return b.smesherID
}

// Fields returns an array of LoggableFields for logging.
func (b *Ballot) Fields() []log.LoggableField {
	var (
		activeSetSize = 0
		beacon        Beacon
	)
	if b.EpochData != nil {
		activeSetSize = len(b.EpochData.ActiveSet)
		beacon = b.EpochData.Beacon
	}
	return []log.LoggableField{
		b.ID(),
		b.LayerIndex,
		b.LayerIndex.GetEpoch(),
		log.FieldNamed("smesher_id", b.SmesherID()),
		log.FieldNamed("base_ballot", b.BaseBallot),
		log.Int("supports", len(b.ForDiff)),
		log.Int("againsts", len(b.AgainstDiff)),
		log.Int("abstains", len(b.NeutralDiff)),
		b.AtxID,
		log.Uint32("eligibility_counter", b.EligibilityProof.J),
		log.FieldNamed("ref_ballot", b.RefBallot),
		log.Int("active_set_size", activeSetSize),
		log.String("beacon", beacon.ShortString()),
	}
}

// MarshalLogObject implements logging encoder for Ballot.
func (b *Ballot) MarshalLogObject(encoder log.ObjectEncoder) error {
	var (
		activeSetSize = 0
		beacon        Beacon
	)

	if b.EpochData != nil {
		activeSetSize = len(b.EpochData.ActiveSet)
		beacon = b.EpochData.Beacon
	}

	encoder.AddString("id", b.ID().String())
	encoder.AddUint32("layer", b.LayerIndex.Value)
	encoder.AddUint32("epoch", uint32(b.LayerIndex.GetEpoch()))
	encoder.AddString("smesher", b.SmesherID().String())
	encoder.AddString("base_ballot", b.BaseBallot.String())
	encoder.AddInt("supports", len(b.ForDiff))
	encoder.AddInt("againsts", len(b.AgainstDiff))
	encoder.AddInt("abstains", len(b.NeutralDiff))
	encoder.AddString("atx", b.AtxID.String())
	encoder.AddUint32("eligibility_counter", b.EligibilityProof.J)
	encoder.AddString("ref_ballot", b.RefBallot.String())
	encoder.AddInt("active_set_size", activeSetSize)
	encoder.AddString("beacon", beacon.ShortString())
	return nil
}

// ToBallotIDs turns a list of Ballot into a list of BallotID.
func ToBallotIDs(ballots []*Ballot) []BallotID {
	ids := make([]BallotID, 0, len(ballots))
	for _, b := range ballots {
		ids = append(ids, b.ID())
	}
	return ids
}

// String returns a short prefix of the hex representation of the ID.
func (id BallotID) String() string {
	return id.AsHash32().ShortString()
}

// Bytes returns the BallotID as a byte slice.
func (id BallotID) Bytes() []byte {
	return id.AsHash32().Bytes()
}

// AsHash32 returns a Hash32 whose first 20 bytes are the bytes of this BallotID, it is right-padded with zeros.
func (id BallotID) AsHash32() Hash32 {
	return Hash20(id).ToHash32()
}

// Field returns a log field. Implements the LoggableField interface.
func (id BallotID) Field() log.Field {
	return log.String("ballot_id", id.String())
}

// Compare returns true if other (the given BallotID) is less than this BallotID, by lexicographic comparison.
func (id BallotID) Compare(other BallotID) bool {
	return bytes.Compare(id.Bytes(), other.Bytes()) < 0
}

// BallotIDsToHashes turns a list of BallotID into their Hash32 representation.
func BallotIDsToHashes(ids []BallotID) []Hash32 {
	hashes := make([]Hash32, 0, len(ids))
	for _, id := range ids {
		hashes = append(hashes, id.AsHash32())
	}
	return hashes
}

// DBBallot is a Ballot structure as it is stored in DB.
type DBBallot struct {
	InnerBallot
	// NOTE(dshulyak) this is a bit redundant to store ID here as well but less likely
	// to break if in future key for database will be changed
	ID        BallotID
	Signature []byte
	SmesherID []byte // derived from signature when ballot is received
}

// ToBallot creates a Ballot from data that is stored locally.
func (b *DBBallot) ToBallot() *Ballot {
	return &Ballot{
		ballotID:    b.ID,
		InnerBallot: b.InnerBallot,
		Signature:   b.Signature,
		smesherID:   signing.NewPublicKey(b.SmesherID),
	}
}