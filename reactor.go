package hotstuff2

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmttypes "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	"github.com/cometbft/cometbft/types"
)

type ConsensusReactor struct {
	proxyApp     proxy.AppConns
	height       int64
	proposerAddr []byte
	appHash      []byte
	genDoc       *types.GenesisDoc
}

func NewConsensusReactor(proxyApp proxy.AppConns, height int64, genDoc *types.GenesisDoc) *ConsensusReactor {
	proposerAddr, _ := hex.DecodeString("59CD8C9232F9C6576FCB4BF7BD8551B24D2172E7")
	return &ConsensusReactor{proxyApp: proxyApp, height: height, proposerAddr: proposerAddr, genDoc: genDoc}
}

func (c *ConsensusReactor) Run(ctx context.Context) {
	timer := time.NewTicker(2 * time.Second)
	infoRes := c.CallInfo(ctx)
	c.appHash = infoRes.GetLastBlockAppHash()

	if infoRes.LastBlockHeight == 0 {
		c.CallInitChain(ctx, infoRes, c.genDoc)
	}
	fmt.Println("last block height: ", infoRes.LastBlockHeight)
	c.height = infoRes.LastBlockHeight

	for {
		select {
		case <-timer.C:
			proposalRes := c.CallPrepareProposal(ctx, c.height+1)
			processRes := c.CallProcessProposal(ctx, c.height+1, proposalRes.GetTxs())
			fmt.Println(processRes.GetStatus().String())
			b := make([]byte, 8)
			binary.LittleEndian.PutUint64(b, uint64(c.height+1))
			finalizeRes := c.CallFinalizeBlock(ctx, c.height+1, proposalRes.GetTxs(), b)
			fmt.Println("app hash:", hex.EncodeToString(finalizeRes.AppHash))
			c.CallCommit(ctx)
			c.height++

		case <-ctx.Done():
			return
		}
	}
}

func (c *ConsensusReactor) CallInfo(ctx context.Context) *abcitypes.ResponseInfo {
	req := &abcitypes.RequestInfo{}
	res, err := c.proxyApp.Query().Info(ctx, req)
	if err != nil {
		fmt.Println("error: ", err)
	}
	return res
}

func (c *ConsensusReactor) CallInitChain(ctx context.Context, infoRes *abcitypes.ResponseInfo, genDoc *types.GenesisDoc) *abcitypes.ResponseInitChain {
	validators := make([]*types.Validator, len(genDoc.Validators))
	for i, val := range genDoc.Validators {
		validators[i] = types.NewValidator(val.PubKey, val.Power)
	}
	validatorSet := types.NewValidatorSet(validators)
	nextVals := types.TM2PB.ValidatorUpdates(validatorSet)
	pbparams := genDoc.ConsensusParams.ToProto()

	req := &abcitypes.RequestInitChain{
		Time:            time.Now(),
		ChainId:         genDoc.ChainID,
		InitialHeight:   genDoc.InitialHeight,
		ConsensusParams: &pbparams,
		Validators:      nextVals,
		AppStateBytes:   genDoc.AppState,
	}
	res, err := c.proxyApp.Consensus().InitChain(ctx, req)
	if err != nil {
		fmt.Println("error: ", err)
	}
	return res
}

func (c *ConsensusReactor) CallPrepareProposal(ctx context.Context, height int64) *abcitypes.ResponsePrepareProposal {
	req := &abcitypes.RequestPrepareProposal{
		Txs:             make([][]byte, 0),
		MaxTxBytes:      94371840,
		Height:          height,
		ProposerAddress: c.proposerAddr,
	}
	res, err := c.proxyApp.Consensus().PrepareProposal(ctx, req)
	if err != nil {
		fmt.Println("error: ", err)
	}
	return res
}

func (c *ConsensusReactor) CallProcessProposal(ctx context.Context, height int64, txs [][]byte) *abcitypes.ResponseProcessProposal {
	votes := make([]abcitypes.VoteInfo, 0)
	votes = append(votes, abcitypes.VoteInfo{Validator: abcitypes.Validator{Address: c.proposerAddr, Power: 1}, BlockIdFlag: cmttypes.BlockIDFlagCommit})
	lastCommit := abcitypes.CommitInfo{Round: 0, Votes: votes}
	req := &abcitypes.RequestProcessProposal{
		Txs:                txs,
		Height:             height,
		ProposerAddress:    c.proposerAddr,
		ProposedLastCommit: lastCommit,
	}
	res, err := c.proxyApp.Consensus().ProcessProposal(ctx, req)
	if err != nil {
		fmt.Println("error: ", err)
	}
	return res
}

func (c *ConsensusReactor) CallFinalizeBlock(ctx context.Context, height int64, txs [][]byte, hash []byte) *abcitypes.ResponseFinalizeBlock {
	req := &abcitypes.RequestFinalizeBlock{
		Txs:             txs,
		Hash:            hash,
		Height:          height,
		ProposerAddress: c.proposerAddr,
	}
	res, err := c.proxyApp.Consensus().FinalizeBlock(ctx, req)
	if err != nil {
		fmt.Println("error: ", err)
	}
	return res
}

func (c *ConsensusReactor) CallCommit(ctx context.Context) *abcitypes.ResponseCommit {
	res, err := c.proxyApp.Consensus().Commit(ctx)
	if err != nil {
		fmt.Println("error: ", err)
	}
	return res
}
