package common

import (
	"bytes"

	blsu "github.com/protolambda/bls12-381-util"
)

type BeaconBlockEnvelope struct {
	ForkDigest ForkDigest

	// Block header details
	BeaconBlockHeader

	// Fork-specific block body
	Body SpecObj

	// Cached block root (hash-tree-root of Message)
	BlockRoot Root

	// Block signature
	Signature BLSSignature
}

func (b *BeaconBlockEnvelope) VerifySignature(spec *Spec, genesisValidatorsRoot Root, proposer ValidatorIndex, pub *CachedPubkey) bool {
	version := spec.ForkVersion(b.Slot)
	return b.VerifySignatureVersioned(spec, version, genesisValidatorsRoot, proposer, pub)
}

// VerifySignatureWithForkDigest verifies the signature using the fork digest already present in the envelope
func (b *BeaconBlockEnvelope) VerifySignatureWithForkDigest(spec *Spec, genesisValidatorsRoot Root, proposer ValidatorIndex, pub *CachedPubkey) bool {
	if b.ProposerIndex != proposer {
		return false
	}
	
	// The fork digest encodes both the fork version and genesis validators root.
	// We need to find which fork version was used to create this digest.
	versions := []Version{
		spec.GENESIS_FORK_VERSION,
		spec.ALTAIR_FORK_VERSION,
		spec.BELLATRIX_FORK_VERSION,
		spec.CAPELLA_FORK_VERSION,
		spec.DENEB_FORK_VERSION,
		spec.ELECTRA_FORK_VERSION,
	}
	
	// Try each version to find which one produces the matching fork digest
	for _, version := range versions {
		digest := ComputeForkDigest(version, genesisValidatorsRoot)
		if digest == b.ForkDigest {
			// Found the matching version, now verify the signature
			pubKey, err := pub.Pubkey()
			if err != nil {
				return false
			}
			dom := ComputeDomain(DOMAIN_BEACON_PROPOSER, version, genesisValidatorsRoot)
			signingRoot := ComputeSigningRoot(b.BlockRoot, dom)
			sig, err := b.Signature.Signature()
			if err != nil {
				return false
			}
			return blsu.Verify(pubKey, signingRoot[:], sig)
		}
	}
	
	// Fork digest doesn't match any known version
	return false
}

func (b *BeaconBlockEnvelope) VerifySignatureVersioned(spec *Spec, version Version, genesisValidatorsRoot Root, proposer ValidatorIndex, cachedPub *CachedPubkey) bool {
	if b.ProposerIndex != proposer {
		return false
	}
	forkRoot := ComputeForkDataRoot(version, genesisValidatorsRoot)
	// Sanity check fork digest
	if !bytes.Equal(forkRoot[0:4], b.ForkDigest[:]) {
		return false
	}
	pub, err := cachedPub.Pubkey()
	if err != nil {
		return false
	}
	dom := ComputeDomain(DOMAIN_BEACON_PROPOSER, version, genesisValidatorsRoot)
	signingRoot := ComputeSigningRoot(b.BlockRoot, dom)
	sig, err := b.Signature.Signature()
	if err != nil {
		return false
	}
	return blsu.Verify(pub, signingRoot[:], sig)
}

type EnvelopeBuilder interface {
	Envelope(spec *Spec, digest ForkDigest) *BeaconBlockEnvelope
}
