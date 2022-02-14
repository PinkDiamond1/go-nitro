package ledger

import (
	"errors"
	"fmt"
	"math/big"

	"github.com/statechannels/go-nitro/channel"
	"github.com/statechannels/go-nitro/channel/state"
	"github.com/statechannels/go-nitro/channel/state/outcome"
	"github.com/statechannels/go-nitro/protocols"
	"github.com/statechannels/go-nitro/types"
)

type LedgerCranker struct {
	ledgers map[types.Destination]*channel.TwoPartyLedger
	nonce   *big.Int
}

func NewLedgerCranker() LedgerCranker {
	return LedgerCranker{
		ledgers: make(map[types.Destination]*channel.TwoPartyLedger),
		nonce:   big.NewInt(0),
	}
}

// Update updates the ledger cranker with the given ledger channel
// Eventually this will be deprecated in favour of using store
func (l *LedgerCranker) Update(ledger *channel.TwoPartyLedger) {
	l.ledgers[ledger.Id] = ledger
}

// CreateLedger creates a new  two party ledger channel based on the provided left and right outcomes.
func (l *LedgerCranker) CreateLedger(left outcome.Allocation, right outcome.Allocation, secretKey *[]byte, myIndex uint) *channel.TwoPartyLedger {

	leftAddress, _ := left.Destination.ToAddress()
	rightAddress, _ := right.Destination.ToAddress()
	initialState := state.State{
		ChainId:           big.NewInt(9001),
		Participants:      []types.Address{leftAddress, rightAddress},
		ChannelNonce:      l.nonce,
		AppDefinition:     types.Address{},
		ChallengeDuration: big.NewInt(45),
		AppData:           []byte{},
		Outcome: outcome.Exit{outcome.SingleAssetExit{
			Allocations: outcome.Allocations{left, right},
		}},
		TurnNum: 0,
		IsFinal: false,
	}

	ledger, lErr := channel.NewTwoPartyLedger(initialState, myIndex)
	if lErr != nil {
		panic(lErr)
	}

	l.ledgers[ledger.Id] = ledger
	// Update the nonce by 1
	l.nonce = big.NewInt(0).Add(l.nonce, big.NewInt(1))
	return ledger
}

// HandleRequest accepts a ledger request and updates the ledger channel based on the request.
// It returns a signed state message that can be sent to other participants.
func (l *LedgerCranker) HandleRequest(request protocols.LedgerRequest, oId protocols.ObjectiveId, secretKey *[]byte) (protocols.SideEffects, error) {

	ledger := l.GetLedger(request.LedgerId)
	guarantee, _ := outcome.GuaranteeMetadata{
		Left:  request.Left,
		Right: request.Right,
	}.Encode()

	supported, err := ledger.Channel.LatestSupportedState()
	if err != nil {
		return protocols.SideEffects{}, fmt.Errorf("Could not find a supported state %w", err)
	}

	asset := types.Address{}
	nextState := supported.Clone()

	// Calculate the amounts
	amountPerParticipant := big.NewInt(0).Div(request.Amount[asset], big.NewInt(2))
	leftAmount := big.NewInt(0).Sub(nextState.Outcome.TotalAllocatedFor(request.Left)[asset], amountPerParticipant)
	rightAmount := big.NewInt(0).Sub(nextState.Outcome.TotalAllocatedFor(request.Right)[asset], amountPerParticipant)
	if leftAmount.Cmp(big.NewInt(0)) < 0 {
		return protocols.SideEffects{}, fmt.Errorf("Allocation for %x cannot afford the amount %d", request.Left, amountPerParticipant)
	}
	if rightAmount.Cmp(big.NewInt(0)) < 0 {
		return protocols.SideEffects{}, fmt.Errorf("Allocation for %x cannot afford the amount %d", request.Right, amountPerParticipant)
	}

	nextState.Outcome = outcome.Exit{outcome.SingleAssetExit{
		Allocations: outcome.Allocations{
			outcome.Allocation{
				Destination: request.Left,
				Amount:      leftAmount,
			},
			outcome.Allocation{
				Destination: request.Right,
				Amount:      rightAmount,
			},
			outcome.Allocation{
				Destination:    request.Destination,
				Amount:         request.Amount[types.Address{}],
				AllocationType: outcome.GuaranteeAllocationType,
				Metadata:       guarantee,
			},
		},
	}}

	nextState.TurnNum = nextState.TurnNum + 1

	ss := state.NewSignedState(nextState)
	err = ss.SignAndAdd(secretKey)
	if err != nil {
		return protocols.SideEffects{}, fmt.Errorf("Could not sign state: %w", err)
	}
	if ok := ledger.Channel.AddSignedState(ss); !ok {
		return protocols.SideEffects{}, errors.New("Could not add signed state to channel")
	}

	messages := protocols.CreateSignedStateMessages(oId, ss, ledger.MyIndex)
	return protocols.SideEffects{MessagesToSend: messages}, nil

}

// GetLedger returns the ledger for the given id.
// This will be deprecated in favour of using the store
func (l *LedgerCranker) GetLedger(ledgerId types.Destination) *channel.TwoPartyLedger {
	ledger, ok := l.ledgers[ledgerId]
	if !ok {
		panic(fmt.Sprintf("Ledger %s not found", ledgerId))
	}
	return ledger
}

func SignPreAndPostFundingStates(ledger *channel.TwoPartyLedger, secretKeys []*[]byte) {
	for _, sk := range secretKeys {
		_, _ = ledger.SignAndAddPrefund(sk)
	}
	for _, sk := range secretKeys {
		_, _ = ledger.Channel.SignAndAddPostfund(sk)
	}
}

func SignLatest(ledger *channel.TwoPartyLedger, secretKeys [][]byte) {

	// Find the largest turn num and therefore the latest state
	turnNum := uint64(0)
	for t := range ledger.SignedStateForTurnNum {
		if t > turnNum {
			turnNum = t
		}
	}
	// Sign it
	toSign := ledger.SignedStateForTurnNum[turnNum]
	for _, secretKey := range secretKeys {
		_ = toSign.SignAndAdd(&secretKey)
	}
	ledger.Channel.AddSignedState(toSign)
}
