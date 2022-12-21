package genesis

import (
	"fmt"
	"math/big"

	"github.com/ethereum-optimism/optimism/op-bindings/bindings"

	"github.com/ethereum-optimism/optimism/op-bindings/predeploys"
	"github.com/ethereum-optimism/optimism/op-chain-ops/immutables"
	"github.com/ethereum-optimism/optimism/op-chain-ops/state"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/log"
)

// UntouchablePredeploys are addresses in the predeploy namespace
// that should not be touched by the migration process.
var UntouchablePredeploys = map[common.Address]bool{
	predeploys.LegacyMessagePasserAddr: true,
	predeploys.DeployerWhitelistAddr:   true,
	predeploys.GovernanceTokenAddr:     true,
	predeploys.L1BlockNumberAddr:       true,
	predeploys.WETH9Addr:               true,
}

// UntouchableCodeHashes contains code hashes of all the contracts
// that should not be touched by the migration process.
var UntouchableCodeHashes = map[common.Address]common.Hash{
	predeploys.LegacyMessagePasserAddr: common.HexToHash("0xc89b4b2237442578842fd73dfe24a34f3d19a8db05719dd7938f04574b4e89e6"),
	predeploys.DeployerWhitelistAddr:   common.HexToHash("0x76cbd4bfafd893280d1490c285ae1919ca2bdbed81b6a1eea7404207ede7d02e"),
	predeploys.GovernanceTokenAddr:     common.HexToHash("0xc4a213cf5f06418533e5168d8d82f7ccbcc97f27ab90197c2c051af6a4941cf9"),
	predeploys.L1BlockNumberAddr:       common.HexToHash("0xfab90d3a32b245623bee66d280f816968575ddcdef1f69452a0beeba519b190b"),
	predeploys.WETH9Addr:               common.HexToHash("0x779bbf2a738ef09d961c945116197e2ac764c1b39304b2b4418cd4e42668b173"),
}

// FundDevAccounts will fund each of the development accounts.
func FundDevAccounts(db vm.StateDB) {
	for _, account := range DevAccounts {
		db.CreateAccount(account)
		db.AddBalance(account, devBalance)
	}
}

// SetL2Proxies will set each of the proxies in the state. It requires
// a Proxy and ProxyAdmin deployment present so that the Proxy bytecode
// can be set in state and the ProxyAdmin can be set as the admin of the
// Proxy.
func SetL2Proxies(db vm.StateDB) error {
	return setProxies(db, predeploys.ProxyAdminAddr, bigL2PredeployNamespace, 2048)
}

// SetL1Proxies will set each of the proxies in the state. It requires
// a Proxy and ProxyAdmin deployment present so that the Proxy bytecode
// can be set in state and the ProxyAdmin can be set as the admin of the
// Proxy.
func SetL1Proxies(db vm.StateDB, proxyAdminAddr common.Address) error {
	return setProxies(db, proxyAdminAddr, bigL1PredeployNamespace, 2048)
}

func setProxies(db vm.StateDB, proxyAdminAddr common.Address, namespace *big.Int, count uint64) error {
	depBytecode, err := bindings.GetDeployedBytecode("Proxy")
	if err != nil {
		return err
	}

	for i := uint64(0); i <= count; i++ {
		bigAddr := new(big.Int).Or(namespace, new(big.Int).SetUint64(i))
		addr := common.BigToAddress(bigAddr)

		if UntouchablePredeploys[addr] || addr == predeploys.ProxyAdminAddr {
			log.Info("Skipping setting proxy", "address", addr)
			continue
		}

		if !db.Exist(addr) {
			db.CreateAccount(addr)
		}

		db.SetCode(addr, depBytecode)
		db.SetState(addr, AdminSlot, proxyAdminAddr.Hash())
		log.Trace("Set proxy", "address", addr, "admin", proxyAdminAddr)
	}
	return nil
}

func SetLegacyETH(db vm.StateDB, storage state.StorageConfig, immutable immutables.ImmutableConfig) error {
	deployResults, err := immutables.BuildOptimism(immutable)
	if err != nil {
		return err
	}

	return setupPredeploy(db, deployResults, storage, "LegacyERC20ETH", predeploys.LegacyERC20ETHAddr, predeploys.LegacyERC20ETHAddr)
}

// SetImplementations will set the implmentations of the contracts in the state
// and configure the proxies to point to the implementations. It also sets
// the appropriate storage values for each contract at the proxy address.
func SetImplementations(db vm.StateDB, storage state.StorageConfig, immutable immutables.ImmutableConfig) error {
	deployResults, err := immutables.BuildOptimism(immutable)
	if err != nil {
		return err
	}

	for name, address := range predeploys.Predeploys {
		if UntouchablePredeploys[*address] {
			continue
		}

		if *address == predeploys.LegacyERC20ETHAddr {
			continue
		}

		codeAddr, err := AddressToCodeNamespace(*address)
		if err != nil {
			return fmt.Errorf("error converting to code namespace: %w", err)
		}

		db.CreateAccount(codeAddr)

		// Proxy admin is a special case - it needs an impl set, but at its own address
		if *address == predeploys.ProxyAdminAddr {
			codeAddr = *address
		} else {
			db.SetState(*address, ImplementationSlot, codeAddr.Hash())
		}

		if err := setupPredeploy(db, deployResults, storage, name, *address, codeAddr); err != nil {
			return err
		}

		code := db.GetCode(codeAddr)
		if len(code) == 0 {
			return fmt.Errorf("code not set for %s", name)
		}
	}
	return nil
}

// SetPrecompileBalances will set a single wei at each precompile address.
// This is an optimization to make calling them cheaper. This should only
// be used for devnets.
func SetPrecompileBalances(db vm.StateDB) {
	for i := 0; i < 256; i++ {
		addr := common.BytesToAddress([]byte{byte(i)})
		db.CreateAccount(addr)
		db.AddBalance(addr, common.Big1)
	}
}

func setupPredeploy(db vm.StateDB, deployResults immutables.DeploymentResults, storage state.StorageConfig, name string, proxyAddr common.Address, implAddr common.Address) error {
	// Use the generated bytecode when there are immutables
	// otherwise use the artifact deployed bytecode
	if bytecode, ok := deployResults[name]; ok {
		log.Info("Setting deployed bytecode with immutables", "name", name, "address", implAddr)
		db.SetCode(implAddr, bytecode)
	} else {
		depBytecode, err := bindings.GetDeployedBytecode(name)
		if err != nil {
			return err
		}
		log.Info("Setting deployed bytecode from solc compiler output", "name", name, "address", implAddr)
		db.SetCode(implAddr, depBytecode)
	}

	// Set the storage values
	if storageConfig, ok := storage[name]; ok {
		log.Info("Setting storage", "name", name, "address", proxyAddr)
		if err := state.SetStorage(name, proxyAddr, storageConfig, db); err != nil {
			return err
		}
	}

	return nil
}
