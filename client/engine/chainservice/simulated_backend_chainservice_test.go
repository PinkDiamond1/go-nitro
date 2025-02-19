package chainservice

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/google/go-cmp/cmp"
	"github.com/statechannels/go-nitro/channel/state"
	"github.com/statechannels/go-nitro/channel/state/outcome"
	"github.com/statechannels/go-nitro/internal/testactors"
	"github.com/statechannels/go-nitro/protocols"
	"github.com/statechannels/go-nitro/types"
)

var (
	Alice = testactors.Alice
	Bob   = testactors.Bob
)

var concludeOutcome = outcome.Exit{
	outcome.SingleAssetExit{
		Asset: types.Address{},
		Allocations: outcome.Allocations{
			outcome.Allocation{
				Destination: types.AddressToDestination(common.HexToAddress(`0xF5A1BB5607C9D079E46d1B3Dc33f257d937b43BD`)),
				Amount:      big.NewInt(1),
			},
			outcome.Allocation{
				Destination: types.AddressToDestination(common.HexToAddress(`0xEe18fF1575055691009aa246aE608132C57a422c`)),
				Amount:      big.NewInt(1),
			},
		},
	},
}

type NoopLogger struct{}

func (l NoopLogger) Write(p []byte) (n int, err error) {
	return 0, nil
}

func TestDepositSimulatedBackendChainService(t *testing.T) {
	one := big.NewInt(1)
	sim, bindings, ethAccounts, err := SetupSimulatedBackend(1)
	if err != nil {
		t.Fatal(err)
	}

	cs, err := NewSimulatedBackendChainService(sim, bindings, ethAccounts[0], NoopLogger{})
	if err != nil {
		t.Fatal(err)
	}

	// Prepare test data to trigger EthChainService
	testDeposit := types.Funds{
		common.HexToAddress("0x00"): one,
		bindings.Token.Address:      one,
	}
	channelID := types.Destination(common.HexToHash(`4ebd366d014a173765ba1e50f284c179ade31f20441bec41664712aac6cc461d`))
	testTx := protocols.NewDepositTransaction(channelID, testDeposit)

	out := cs.EventFeed()
	// Submit transactiom
	err = cs.SendTransaction(testTx)
	if err != nil {
		t.Fatal(err)
	}

	// Check that the recieved events matches the expected event
	for i := 0; i < 2; i++ {
		receivedEvent := <-out
		dEvent := receivedEvent.(DepositedEvent)
		expectedEvent := NewDepositedEvent(channelID, 2, dEvent.AssetAddress, testDeposit[dEvent.AssetAddress], testDeposit[dEvent.AssetAddress])
		if diff := cmp.Diff(expectedEvent, dEvent, cmp.AllowUnexported(DepositedEvent{}, commonEvent{}, big.Int{})); diff != "" {
			t.Fatalf("Received event did not match expectation; (-want +got):\n%s", diff)
		}
		delete(testDeposit, dEvent.AssetAddress)
	}

	if len(testDeposit) != 0 {
		t.Fatalf("Mismatch between the deposit transaction and the received events")
	}

	sim.Close()
}

func TestConcludeSimulatedBackendChainService(t *testing.T) {

	sim, bindings, ethAccounts, err := SetupSimulatedBackend(1)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := NewSimulatedBackendChainService(sim, bindings, ethAccounts[0], NoopLogger{})
	if err != nil {
		t.Fatal(err)
	}
	out := cs.EventFeed()

	var concludeState = state.State{
		ChainId: big.NewInt(1337),
		Participants: []types.Address{
			Alice.Address(),
			Bob.Address(),
		},
		ChannelNonce:      37140676580,
		AppDefinition:     bindings.ConsensusApp.Address,
		ChallengeDuration: 0,
		AppData:           []byte{},
		Outcome:           concludeOutcome,
		TurnNum:           uint64(2),
		IsFinal:           true,
	}

	// Generate Signatures
	aSig, _ := concludeState.Sign(Alice.PrivateKey)
	bSig, _ := concludeState.Sign(Bob.PrivateKey)

	// Fund channel
	testDeposit := types.Funds{
		common.HexToAddress("0x00"): big.NewInt(3),
	}
	cId := concludeState.ChannelId()

	depositTx := protocols.NewDepositTransaction(cId, testDeposit)
	err = cs.SendTransaction(depositTx)
	if err != nil {
		t.Fatal(err)
	}
	<-out

	signedConcludeState := state.NewSignedState(concludeState)
	err = signedConcludeState.AddSignature(aSig)
	if err != nil {
		t.Fatal(err)
	}
	err = signedConcludeState.AddSignature(bSig)
	if err != nil {
		t.Fatal(err)
	}
	concludeTx := protocols.NewWithdrawAllTransaction(cId, signedConcludeState)
	err = cs.SendTransaction(concludeTx)
	if err != nil {
		t.Fatal(err)
	}
	// Check that the recieved event matches the expected event
	concludedEvent := <-out
	expectedEvent := ConcludedEvent{commonEvent: commonEvent{channelID: cId, BlockNum: 3}}
	if diff := cmp.Diff(expectedEvent, concludedEvent, cmp.AllowUnexported(ConcludedEvent{}, commonEvent{})); diff != "" {
		t.Fatalf("Received event did not match expectation; (-want +got):\n%s", diff)
	}

	// Check that the recieved event matches the expected event
	allocationUpdatedEvent := <-out
	expectedEvent2 := NewAllocationUpdatedEvent(cId, 3, common.Address{}, new(big.Int).SetInt64(1))

	if diff := cmp.Diff(expectedEvent2, allocationUpdatedEvent, cmp.AllowUnexported(AllocationUpdatedEvent{}, commonEvent{}, big.Int{})); diff != "" {
		t.Fatalf("Received event did not match expectation; (-want +got):\n%s", diff)
	}

	// Inspect state of chain (call StatusOf)
	statusOnChain, err := bindings.Adjudicator.Contract.StatusOf(&bind.CallOpts{}, cId)
	if err != nil {
		t.Fatal(err)
	}

	emptyBytes := [32]byte{}
	// Make assertion
	if !bytes.Equal(statusOnChain[:], emptyBytes[:]) {
		t.Fatalf("Adjudicator not updated as expected, got %v wanted %v", common.Bytes2Hex(statusOnChain[:]), common.Bytes2Hex(emptyBytes[:]))
	}

	// Not sure if this is necessary
	sim.Close()
}
