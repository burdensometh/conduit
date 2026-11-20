package anonymizer

import (
	sdk "github.com/algorand/go-algorand-sdk/v2/types"

	"github.com/algorand/conduit/conduit/data"
)

// Role names a group of address-typed fields that can be selected for
// anonymization in the plugin configuration. Every field listed here is
// declared by the Go SDK with the sdk.Address type (or a slice of it); no other
// field is ever inspected, so 32-byte digests such as transaction IDs, group
// hashes, block hashes, leases or state proof commitments are never mistaken
// for addresses.
type Role string

const (
	// RoleSender selects the transaction sender field (snd).
	RoleSender Role = "sender"
	// RoleReceiver selects the payment receiver field (rcv).
	RoleReceiver Role = "receiver"
	// RoleClose selects the payment close-remainder-to field (close).
	RoleClose Role = "close-remainder-to"
	// RoleAuthAddr selects the signed transaction authorized sender field (sgnr).
	RoleAuthAddr Role = "auth-addr"
	// RoleRekeyTo selects the transaction rekey-to field (rekey).
	RoleRekeyTo Role = "rekey-to"
	// RoleAssetSender selects the asset transfer clawback-from field (asnd).
	RoleAssetSender Role = "asset-sender"
	// RoleAssetRecv selects the asset transfer receiver field (arcv).
	RoleAssetRecv Role = "asset-receiver"
	// RoleAssetClose selects the asset transfer close-to field (aclose).
	RoleAssetClose Role = "asset-close-to"
	// RoleFreezeAcct selects the asset freeze account field (fadd).
	RoleFreezeAcct Role = "freeze-account"
	// RoleAssetMgr selects the asset config manager field (apar.m).
	RoleAssetMgr Role = "asset-config-manager"
	// RoleAssetReserve selects the asset config reserve field (apar.r).
	RoleAssetReserve Role = "asset-config-reserve"
	// RoleAssetFreeze selects the asset config freeze field (apar.f).
	RoleAssetFreeze Role = "asset-config-freeze"
	// RoleAssetClaw selects the asset config clawback field (apar.c).
	RoleAssetClaw Role = "asset-config-clawback"
	// RoleAppAccounts selects the application call foreign accounts (apat).
	RoleAppAccounts Role = "application-accounts"
	// RoleAppAccess selects the address entries of the application access list (al).
	RoleAppAccess Role = "application-access"
	// RoleHeartbeat selects the heartbeat address field (hb.a).
	RoleHeartbeat Role = "heartbeat-address"
	// RoleSharedAccts selects the eval-delta shared accounts (sa).
	RoleSharedAccts Role = "shared-accounts"

	// RoleProposer selects the block header proposer field (prp).
	RoleProposer Role = "block-proposer"
	// RoleFeeSink selects the fee sink address (block header fees, genesis fees).
	RoleFeeSink Role = "fee-sink"
	// RoleRewardsPool selects the rewards pool address (block header rwd, genesis rwd).
	RoleRewardsPool Role = "rewards-pool"
	// RoleParticipation selects the block header participation update accounts.
	RoleParticipation Role = "participation-accounts"

	// RoleLedgerAccounts selects the ledger state delta balance records.
	RoleLedgerAccounts Role = "ledger-accounts"
	// RoleLedgerAppResources selects the ledger state delta app resource records.
	RoleLedgerAppResources Role = "ledger-app-resources"
	// RoleLedgerAssetRes selects the ledger state delta asset resource records.
	RoleLedgerAssetRes Role = "ledger-asset-resources"
	// RoleLedgerTxLeases selects the ledger state delta transaction lease senders.
	RoleLedgerTxLeases Role = "ledger-tx-leases"
	// RoleLedgerCreatables selects the ledger state delta creatable creators.
	RoleLedgerCreatables Role = "ledger-creatables"

	// RoleGenesisAllocations selects the genesis allocation addresses.
	RoleGenesisAllocations Role = "genesis-allocations"
)

// AllRoles is the default set: every address-typed field is anonymized.
var AllRoles = []Role{
	RoleSender,
	RoleReceiver,
	RoleClose,
	RoleAuthAddr,
	RoleRekeyTo,
	RoleAssetSender,
	RoleAssetRecv,
	RoleAssetClose,
	RoleFreezeAcct,
	RoleAssetMgr,
	RoleAssetReserve,
	RoleAssetFreeze,
	RoleAssetClaw,
	RoleAppAccounts,
	RoleAppAccess,
	RoleHeartbeat,
	RoleSharedAccts,
	RoleProposer,
	RoleFeeSink,
	RoleRewardsPool,
	RoleParticipation,
	RoleLedgerAccounts,
	RoleLedgerAppResources,
	RoleLedgerAssetRes,
	RoleLedgerTxLeases,
	RoleLedgerCreatables,
	RoleGenesisAllocations,
}

// roleSet is the set of address roles selected by the configuration.
type roleSet map[Role]bool

func (r roleSet) has(role Role) bool {
	return r[role]
}

// transformer applies a dataset mapping to the address fields selected by a
// role set.
type transformer struct {
	mapper *addressMapper
	roles  roleSet
}

// transformBlock returns a copy of blk with the selected SDK-typed address
// fields replaced by their dataset pseudonyms. The input value is not modified.
func (t *transformer) transformBlock(blk data.BlockData) data.BlockData {
	out := blk
	out.BlockHeader = t.transformBlockHeader(blk.BlockHeader)
	out.Payset = t.transformPayset(blk.Payset)
	if blk.Delta != nil {
		delta := t.transformLedgerStateDelta(*blk.Delta)
		out.Delta = &delta
	}
	return out
}

// transformBlockHeader rewrites the account addresses carried by a block
// header. The round, genesis ID, genesis hash, seeds and transaction
// commitments are left untouched: they record the origin of the data.
func (t *transformer) transformBlockHeader(hdr sdk.BlockHeader) sdk.BlockHeader {
	if t.roles.has(RoleProposer) {
		hdr.Proposer = t.mapper.mapAddress(hdr.Proposer)
	}
	if t.roles.has(RoleFeeSink) {
		hdr.FeeSink = t.mapper.mapAddress(hdr.FeeSink)
	}
	if t.roles.has(RoleRewardsPool) {
		hdr.RewardsPool = t.mapper.mapAddress(hdr.RewardsPool)
	}
	if t.roles.has(RoleParticipation) {
		hdr.ExpiredParticipationAccounts = t.mapper.mapAddresses(hdr.ExpiredParticipationAccounts)
		hdr.AbsentParticipationAccounts = t.mapper.mapAddresses(hdr.AbsentParticipationAccounts)
	}
	return hdr
}

func (t *transformer) transformPayset(payset []sdk.SignedTxnInBlock) []sdk.SignedTxnInBlock {
	if payset == nil {
		return nil
	}
	out := make([]sdk.SignedTxnInBlock, len(payset))
	for i := range payset {
		out[i] = payset[i]
		out[i].SignedTxnWithAD = t.transformSignedTxnWithAD(payset[i].SignedTxnWithAD)
	}
	return out
}

func (t *transformer) transformSignedTxnWithAD(stxn sdk.SignedTxnWithAD) sdk.SignedTxnWithAD {
	stxn.SignedTxn = t.transformSignedTxn(stxn.SignedTxn)
	stxn.ApplyData = t.transformApplyData(stxn.ApplyData)
	return stxn
}

func (t *transformer) transformSignedTxn(stxn sdk.SignedTxn) sdk.SignedTxn {
	if t.roles.has(RoleAuthAddr) {
		stxn.AuthAddr = t.mapper.mapAddress(stxn.AuthAddr)
	}
	stxn.Txn = t.transformTransaction(stxn.Txn)
	return stxn
}

// transformTransaction rewrites the selected address-typed fields of a
// transaction, including those of nested structs (asset params, application
// access, heartbeat). Signature, note, lease, group hash and other opaque
// byte fields are preserved.
func (t *transformer) transformTransaction(txn sdk.Transaction) sdk.Transaction {
	if t.roles.has(RoleSender) {
		txn.Sender = t.mapper.mapAddress(txn.Sender)
	}
	if t.roles.has(RoleRekeyTo) {
		txn.RekeyTo = t.mapper.mapAddress(txn.RekeyTo)
	}

	if t.roles.has(RoleReceiver) {
		txn.Receiver = t.mapper.mapAddress(txn.Receiver)
	}
	if t.roles.has(RoleClose) {
		txn.CloseRemainderTo = t.mapper.mapAddress(txn.CloseRemainderTo)
	}

	if t.roles.has(RoleAssetMgr) {
		txn.AssetParams.Manager = t.mapper.mapAddress(txn.AssetParams.Manager)
	}
	if t.roles.has(RoleAssetReserve) {
		txn.AssetParams.Reserve = t.mapper.mapAddress(txn.AssetParams.Reserve)
	}
	if t.roles.has(RoleAssetFreeze) {
		txn.AssetParams.Freeze = t.mapper.mapAddress(txn.AssetParams.Freeze)
	}
	if t.roles.has(RoleAssetClaw) {
		txn.AssetParams.Clawback = t.mapper.mapAddress(txn.AssetParams.Clawback)
	}

	if t.roles.has(RoleAssetSender) {
		txn.AssetSender = t.mapper.mapAddress(txn.AssetSender)
	}
	if t.roles.has(RoleAssetRecv) {
		txn.AssetReceiver = t.mapper.mapAddress(txn.AssetReceiver)
	}
	if t.roles.has(RoleAssetClose) {
		txn.AssetCloseTo = t.mapper.mapAddress(txn.AssetCloseTo)
	}

	if t.roles.has(RoleFreezeAcct) {
		txn.FreezeAccount = t.mapper.mapAddress(txn.FreezeAccount)
	}

	if t.roles.has(RoleAppAccounts) {
		txn.Accounts = t.mapper.mapAddresses(txn.Accounts)
	}
	if t.roles.has(RoleAppAccess) && txn.Access != nil {
		access := make([]sdk.ResourceRef, len(txn.Access))
		for i := range txn.Access {
			access[i] = txn.Access[i]
			access[i].Address = t.mapper.mapAddress(txn.Access[i].Address)
		}
		txn.Access = access
	}

	// Heartbeat fields live behind a pointer: clone before mutating so the
	// input transaction is never modified.
	if t.roles.has(RoleHeartbeat) && txn.HeartbeatTxnFields != nil {
		hb := *txn.HeartbeatTxnFields
		hb.HbAddress = t.mapper.mapAddress(hb.HbAddress)
		txn.HeartbeatTxnFields = &hb
	}

	return txn
}

func (t *transformer) transformApplyData(ad sdk.ApplyData) sdk.ApplyData {
	ad.EvalDelta = t.transformEvalDelta(ad.EvalDelta)
	return ad
}

func (t *transformer) transformEvalDelta(ed sdk.EvalDelta) sdk.EvalDelta {
	if t.roles.has(RoleSharedAccts) {
		ed.SharedAccts = t.mapper.mapAddresses(ed.SharedAccts)
	}
	if ed.InnerTxns != nil {
		inner := make([]sdk.SignedTxnWithAD, len(ed.InnerTxns))
		for i := range ed.InnerTxns {
			inner[i] = t.transformSignedTxnWithAD(ed.InnerTxns[i])
		}
		ed.InnerTxns = inner
	}
	// GlobalDelta and LocalDeltas hold opaque application state and are left
	// alone; they are not address-typed SDK fields.
	return ed
}

// transformLedgerStateDelta rewrites the account addresses in a ledger state
// delta. Containers are rebuilt rather than mutated so the delta owned by the
// importer is not modified. Transaction IDs (a hash type, not an address type),
// leases and key/value modifications are preserved.
func (t *transformer) transformLedgerStateDelta(delta sdk.LedgerStateDelta) sdk.LedgerStateDelta {
	out := delta

	// Rebuilding AccountDeltas also drops its unexported lookup caches, which
	// are keyed by the original addresses and would otherwise be inconsistent
	// with the anonymized records.
	var accts sdk.AccountDeltas
	if delta.Accts.Accts != nil {
		accts.Accts = make([]sdk.BalanceRecord, len(delta.Accts.Accts))
		for i, record := range delta.Accts.Accts {
			if t.roles.has(RoleLedgerAccounts) {
				record.Addr = t.mapper.mapAddress(record.Addr)
				record.AuthAddr = t.mapper.mapAddress(record.AuthAddr)
			}
			accts.Accts[i] = record
		}
	}
	if delta.Accts.AppResources != nil {
		accts.AppResources = make([]sdk.AppResourceRecord, len(delta.Accts.AppResources))
		for i, record := range delta.Accts.AppResources {
			if t.roles.has(RoleLedgerAppResources) {
				record.Addr = t.mapper.mapAddress(record.Addr)
				if record.Params.Params != nil {
					params := *record.Params.Params
					params.SizeSponsor = t.mapper.mapAddress(params.SizeSponsor)
					record.Params.Params = &params
				}
			}
			accts.AppResources[i] = record
		}
	}
	if delta.Accts.AssetResources != nil {
		accts.AssetResources = make([]sdk.AssetResourceRecord, len(delta.Accts.AssetResources))
		for i, record := range delta.Accts.AssetResources {
			if t.roles.has(RoleLedgerAssetRes) {
				record.Addr = t.mapper.mapAddress(record.Addr)
				if record.Params.Params != nil {
					params := *record.Params.Params
					params.Manager = t.mapper.mapAddress(params.Manager)
					params.Reserve = t.mapper.mapAddress(params.Reserve)
					params.Freeze = t.mapper.mapAddress(params.Freeze)
					params.Clawback = t.mapper.mapAddress(params.Clawback)
					record.Params.Params = &params
				}
			}
			accts.AssetResources[i] = record
		}
	}
	out.Accts = accts

	if delta.Txleases != nil {
		leases := make(map[sdk.Txlease]sdk.Round, len(delta.Txleases))
		for lease, expiration := range delta.Txleases {
			sender := lease.Sender
			if t.roles.has(RoleLedgerTxLeases) {
				sender = t.mapper.mapAddress(sender)
			}
			leases[sdk.Txlease{Sender: sender, Lease: lease.Lease}] = expiration
		}
		out.Txleases = leases
	}

	if delta.Creatables != nil {
		creatables := make(map[sdk.CreatableIndex]sdk.ModifiedCreatable, len(delta.Creatables))
		for index, creatable := range delta.Creatables {
			if t.roles.has(RoleLedgerCreatables) {
				creatable.Creator = t.mapper.mapAddress(creatable.Creator)
			}
			creatables[index] = creatable
		}
		out.Creatables = creatables
	}

	if delta.Hdr != nil {
		hdr := t.transformBlockHeader(*delta.Hdr)
		out.Hdr = &hdr
	}

	// KvMods, Txids, StateProofNext, PrevTimestamp and Totals are preserved:
	// none of them are address-typed.
	return out
}

// transformGenesis returns a copy of genesis with its address string fields
// replaced by dataset pseudonyms. Genesis stores addresses as strings, so each
// value is parsed (which validates the base32 checksum) and only valid
// addresses are rewritten.
func (t *transformer) transformGenesis(genesis sdk.Genesis) sdk.Genesis {
	out := genesis

	if t.roles.has(RoleFeeSink) {
		out.FeeSink = t.mapAddressString(genesis.FeeSink)
	}
	if t.roles.has(RoleRewardsPool) {
		out.RewardsPool = t.mapAddressString(genesis.RewardsPool)
	}

	if genesis.Allocation != nil {
		out.Allocation = make([]sdk.GenesisAllocation, len(genesis.Allocation))
		for i, allocation := range genesis.Allocation {
			if t.roles.has(RoleGenesisAllocations) {
				allocation.Address = t.mapAddressString(allocation.Address)
			}
			out.Allocation[i] = allocation
		}
	}

	return out
}

func (t *transformer) mapAddressString(address string) string {
	decoded, err := sdk.DecodeAddress(address)
	if err != nil {
		return address
	}
	return t.mapper.mapAddress(decoded).String()
}
