package hotstuff2

import (
	"context"
	"fmt"
	"time"

	"github.com/cometbft/cometbft/blocksync"
	bc "github.com/cometbft/cometbft/blocksync"
	cfg "github.com/cometbft/cometbft/config"
	cs "github.com/cometbft/cometbft/consensus"
	"github.com/cometbft/cometbft/evidence"

	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/service"
	mempl "github.com/cometbft/cometbft/mempool"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/p2p/pex"
	"github.com/cometbft/cometbft/proxy"
	rpccore "github.com/cometbft/cometbft/rpc/core"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/state/txindex"
	"github.com/cometbft/cometbft/state/txindex/null"
	"github.com/cometbft/cometbft/statesync"
	"github.com/cometbft/cometbft/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/version"

	_ "net/http/pprof" //nolint: gosec
)

// Node is the highest level interface to a full CometBFT node.
// It includes all configuration information and running services.
type Node struct {
	service.BaseService

	// config
	config        *cfg.Config
	genesisDoc    *types.GenesisDoc   // initial validator set
	privValidator types.PrivValidator // local node's validator key

	nodeKey *p2p.NodeKey // our node privkey

	// services

	proxyApp proxy.AppConns // connection to the application

	txIndexer txindex.TxIndexer
}

// GenesisDocProvider returns a GenesisDoc.
// It allows the GenesisDoc to be pulled from sources other than the
// filesystem, for instance from a distributed key-value store cluster.
type GenesisDocProvider func() (*types.GenesisDoc, error)

// DefaultGenesisDocProviderFunc returns a GenesisDocProvider that loads
// the GenesisDoc from the config.GenesisFile() on the filesystem.
func DefaultGenesisDocProviderFunc(config *cfg.Config) GenesisDocProvider {
	return func() (*types.GenesisDoc, error) {
		fmt.Println("Genesis file: ", config.GenesisFile())
		genDoc, err := types.GenesisDocFromFile(config.GenesisFile())
		if err != nil {
			fmt.Println("could not load genesis doc", err)
		}
		fmt.Println("chainId: ", genDoc.ChainID)
		fmt.Println("consensus params:", genDoc.ConsensusParams.ToProto())
		fmt.Println(len(genDoc.Validators))
		return types.GenesisDocFromFile(config.GenesisFile())
	}
}

// MetricsProvider returns a consensus, p2p and mempool Metrics.
type MetricsProvider func(chainID string) (*cs.Metrics, *p2p.Metrics, *mempl.Metrics, *sm.Metrics, *proxy.Metrics, *blocksync.Metrics, *statesync.Metrics)

// DefaultMetricsProvider returns Metrics build using Prometheus client library
// if Prometheus is enabled. Otherwise, it returns no-op Metrics.
func DefaultMetricsProvider(config *cfg.InstrumentationConfig) MetricsProvider {
	return func(chainID string) (*cs.Metrics, *p2p.Metrics, *mempl.Metrics, *sm.Metrics, *proxy.Metrics, *blocksync.Metrics, *statesync.Metrics) {
		if config.Prometheus {
			return cs.PrometheusMetrics(config.Namespace, "chain_id", chainID),
				p2p.PrometheusMetrics(config.Namespace, "chain_id", chainID),
				mempl.PrometheusMetrics(config.Namespace, "chain_id", chainID),
				sm.PrometheusMetrics(config.Namespace, "chain_id", chainID),
				proxy.PrometheusMetrics(config.Namespace, "chain_id", chainID),
				blocksync.PrometheusMetrics(config.Namespace, "chain_id", chainID),
				statesync.PrometheusMetrics(config.Namespace, "chain_id", chainID)
		}
		return cs.NopMetrics(), p2p.NopMetrics(), mempl.NopMetrics(), sm.NopMetrics(), proxy.NopMetrics(), blocksync.NopMetrics(), statesync.NopMetrics()
	}
}

// Option sets a parameter for the node.
type Option func(*Node)

// CustomReactors allows you to add custom reactors (name -> p2p.Reactor) to
// the node's Switch.
//
// WARNING: using any name from the below list of the existing reactors will
// result in replacing it with the custom one.
//
//   - MEMPOOL
//   - BLOCKSYNC
//   - CONSENSUS
//   - EVIDENCE
//   - PEX

// NewNode returns a new, ready to go, CometBFT Node.
func NewNode(config *cfg.Config,
	privValidator types.PrivValidator,
	nodeKey *p2p.NodeKey,
	clientCreator proxy.ClientCreator,
	genesisDocProvider GenesisDocProvider,
	dbProvider cfg.DBProvider,
	metricsProvider MetricsProvider,
	logger log.Logger,
	options ...Option,
) (*Node, error) {
	return NewNodeWithContext(context.TODO(), config, privValidator,
		nodeKey, clientCreator, genesisDocProvider, dbProvider,
		metricsProvider, logger, options...)
}

func createAndStartProxyAppConns(clientCreator proxy.ClientCreator, logger log.Logger, metrics *proxy.Metrics) (proxy.AppConns, error) {
	proxyApp := proxy.NewAppConns(clientCreator, metrics)
	proxyApp.SetLogger(logger.With("module", "proxy"))
	if err := proxyApp.Start(); err != nil {
		return nil, fmt.Errorf("error starting proxy app connections: %v", err)
	}
	return proxyApp, nil
}

// NewNodeWithContext is cancellable version of NewNode.
func NewNodeWithContext(ctx context.Context,
	config *cfg.Config,
	privValidator types.PrivValidator,
	nodeKey *p2p.NodeKey,
	clientCreator proxy.ClientCreator,
	genesisDocProvider GenesisDocProvider,
	dbProvider cfg.DBProvider,
	metricsProvider MetricsProvider,
	logger log.Logger,
	options ...Option,
) (*Node, error) {
	genDoc, err := genesisDocProvider()
	if err != nil {
		fmt.Println("gendoc erro: ", err)
	}
	_, _, _, _, abciMetrics, _, _ := metricsProvider(genDoc.ChainID)
	// Create the proxyApp and establish connections to the ABCI app (consensus, mempool, query).
	proxyApp, err := createAndStartProxyAppConns(clientCreator, logger, abciMetrics)
	if err != nil {
		return nil, err
	}

	node := &Node{
		config:        config,
		privValidator: privValidator,

		nodeKey:    nodeKey,
		genesisDoc: genDoc,

		proxyApp: proxyApp,
	}
	node.BaseService = *service.NewBaseService(logger, "Node", node)

	for _, option := range options {
		option(node)
	}

	return node, nil
}

func (n *Node) loop(ctx context.Context) {
	timer := time.NewTicker(2 * time.Second)
	for {
		select {
		case <-timer.C:
			fmt.Println("hello")
			req := &abcitypes.RequestPrepareProposal{
				Txs:        make([][]byte, 0),
				MaxTxBytes: 94371840,
				Height:     1,
			}
			fmt.Println("consnesus: ", n.proxyApp.Consensus())
			if n.proxyApp.Consensus() != nil {
				res, err := n.proxyApp.Consensus().PrepareProposal(ctx, req)
				fmt.Println(err)
				fmt.Println(res)
			}
		case <-ctx.Done():
			return
		}
	}
}

// OnStart starts the Node. It implements service.Service.
func (n *Node) OnStart() error {
	// now := cmttime.Now()
	ctx := context.TODO()
	// genTime := n.genesisDoc.GenesisTime
	// if genTime.After(now) {
	// 	n.Logger.Info("Genesis time is in the future. Sleeping until then...", "genTime", genTime)
	// 	time.Sleep(genTime.Sub(now))
	// }

	reactor := NewConsensusReactor(n.proxyApp, 0, n.genesisDoc)
	go reactor.Run(ctx)
	// go n.loop(ctx)
	return nil
}

// OnStop stops the Node. It implements service.Service.
func (n *Node) OnStop() {
	n.BaseService.OnStop()

	n.Logger.Info("Stopping Node")

}

// ConfigureRPC makes sure RPC has all the objects it needs to operate.
func (n *Node) ConfigureRPC() (*rpccore.Environment, error) {
	pubKey, err := n.privValidator.GetPubKey()
	if pubKey == nil || err != nil {
		return nil, fmt.Errorf("can't get pubkey: %w", err)
	}
	rpcCoreEnv := rpccore.Environment{
		ProxyAppQuery:   n.proxyApp.Query(),
		ProxyAppMempool: n.proxyApp.Mempool(),

		PubKey: pubKey,

		GenDoc:    n.genesisDoc,
		TxIndexer: n.txIndexer,

		Logger: n.Logger.With("module", "rpc"),

		Config: *n.config.RPC,
	}
	if err := rpcCoreEnv.InitGenesisChunks(); err != nil {
		return nil, err
	}
	return &rpcCoreEnv, nil
}

// PrivValidator returns the Node's PrivValidator.
// XXX: for convenience only!
func (n *Node) PrivValidator() types.PrivValidator {
	return n.privValidator
}

// GenesisDoc returns the Node's GenesisDoc.
func (n *Node) GenesisDoc() *types.GenesisDoc {
	return n.genesisDoc
}

// ProxyApp returns the Node's AppConns, representing its connections to the ABCI application.
func (n *Node) ProxyApp() proxy.AppConns {
	return n.proxyApp
}

// Config returns the Node's config.
func (n *Node) Config() *cfg.Config {
	return n.config
}

//------------------------------------------------------------------------------

func (n *Node) Listeners() []string {
	return []string{
		fmt.Sprintf("Listener(@%v)", n.config.P2P.ExternalAddress),
	}
}

func makeNodeInfo(
	config *cfg.Config,
	nodeKey *p2p.NodeKey,
	txIndexer txindex.TxIndexer,
	genDoc *types.GenesisDoc,
	state sm.State,
) (p2p.DefaultNodeInfo, error) {
	txIndexerStatus := "on"
	if _, ok := txIndexer.(*null.TxIndex); ok {
		txIndexerStatus = "off"
	}

	nodeInfo := p2p.DefaultNodeInfo{
		ProtocolVersion: p2p.NewProtocolVersion(
			version.P2PProtocol, // global
			state.Version.Consensus.Block,
			state.Version.Consensus.App,
		),
		DefaultNodeID: nodeKey.ID(),
		Network:       genDoc.ChainID,
		Version:       version.TMCoreSemVer,
		Channels: []byte{
			bc.BlocksyncChannel,
			cs.StateChannel, cs.DataChannel, cs.VoteChannel, cs.VoteSetBitsChannel,
			mempl.MempoolChannel,
			evidence.EvidenceChannel,
			statesync.SnapshotChannel, statesync.ChunkChannel,
		},
		Moniker: config.Moniker,
		Other: p2p.DefaultNodeInfoOther{
			TxIndex:    txIndexerStatus,
			RPCAddress: config.RPC.ListenAddress,
		},
	}

	if config.P2P.PexReactor {
		nodeInfo.Channels = append(nodeInfo.Channels, pex.PexChannel)
	}

	lAddr := config.P2P.ExternalAddress

	if lAddr == "" {
		lAddr = config.P2P.ListenAddress
	}

	nodeInfo.ListenAddr = lAddr

	err := nodeInfo.Validate()
	return nodeInfo, err
}
