// Package directfund implements an off-chain protocol to directly fund a channel.
package directfund // import "github.com/statechannels/go-nitro/directfund"

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/statechannels/go-nitro/channel"
	"github.com/statechannels/go-nitro/channel/state"
	"github.com/statechannels/go-nitro/protocols"
	"github.com/statechannels/go-nitro/types"
)

const (
	WaitingForCompletePrefund  protocols.WaitingFor = "WaitingForCompletePrefund"
	WaitingForMyTurnToFund     protocols.WaitingFor = "WaitingForMyTurnToFund"
	WaitingForCompleteFunding  protocols.WaitingFor = "WaitingForCompleteFunding"
	WaitingForCompletePostFund protocols.WaitingFor = "WaitingForCompletePostFund"
	WaitingForNothing          protocols.WaitingFor = "WaitingForNothing" // Finished
)

const ObjectivePrefix = "DirectFunding-"

func FundOnChainEffect(cId types.Destination, asset string, amount types.Funds) string {
	return "deposit" + amount.String() + "into" + cId.String()
}

// errors
var ErrNotApproved = errors.New("objective not approved")

// Objective is a cache of data computed by reading from the store. It stores (potentially) infinite data
type Objective struct {
	Status protocols.ObjectiveStatus
	C      *channel.Channel

	myDepositSafetyThreshold types.Funds // if the on chain holdings are equal to this amount it is safe for me to deposit
	myDepositTarget          types.Funds // I want to get the on chain holdings up to this much
	fullyFundedThreshold     types.Funds // if the on chain holdings are equal
}

// jsonObjective replaces the directfund.Objective's channel pointer with the
// channel's ID, making jsonObjective suitable for serialization
type jsonObjective struct {
	Status protocols.ObjectiveStatus
	C      types.Destination

	MyDepositSafetyThreshold types.Funds
	MyDepositTarget          types.Funds
	FullyFundedThreshold     types.Funds
}

// NewObjective initiates a Objective with data calculated from
// the supplied initialState and client address
func NewObjective(
	preApprove bool,
	initialState state.State,
	myAddress types.Address,
) (Objective, error) {
	if initialState.TurnNum != 0 {
		return Objective{}, errors.New("cannot construct direct fund objective without prefund state")
	}
	if initialState.IsFinal {
		return Objective{}, errors.New("attempted to initiate new direct-funding objective with IsFinal == true")
	}

	var init = Objective{}
	var err error

	if preApprove {
		init.Status = protocols.Approved
	} else {
		init.Status = protocols.Unapproved
	}

	var myIndex uint
	foundMyAddress := false
	for i, v := range initialState.Participants {
		if v == myAddress {
			myIndex = uint(i)
			foundMyAddress = true
			break
		}
	}
	if !foundMyAddress {
		return Objective{}, errors.New("my address not found in participants")
	}

	init.C = &channel.Channel{}
	init.C, err = channel.New(initialState, myIndex)

	if err != nil {
		return Objective{}, fmt.Errorf("failed to initialize channel for direct-fund objective: %w", err)
	}

	myAllocatedAmount := initialState.Outcome.TotalAllocatedFor(
		types.AddressToDestination(myAddress),
	)

	init.fullyFundedThreshold = initialState.Outcome.TotalAllocated()
	init.myDepositSafetyThreshold = initialState.Outcome.DepositSafetyThreshold(
		types.AddressToDestination(myAddress),
	)
	init.myDepositTarget = init.myDepositSafetyThreshold.Add(myAllocatedAmount)

	return init, nil
}

// Public methods on the DirectFundingObjectiveState

func (o Objective) Id() protocols.ObjectiveId {
	return protocols.ObjectiveId(ObjectivePrefix + o.C.Id.String())
}

func (o Objective) Approve() protocols.Objective {
	updated := o.clone()
	// todo: consider case of s.Status == Rejected
	updated.Status = protocols.Approved

	return &updated
}

func (o Objective) Reject() protocols.Objective {
	updated := o.clone()
	updated.Status = protocols.Rejected
	return &updated
}

// Update receives an ObjectiveEvent, applies all applicable event data to the DirectFundingObjectiveState,
// and returns the updated state
func (o Objective) Update(event protocols.ObjectiveEvent) (protocols.Objective, error) {
	if o.Id() != event.ObjectiveId {
		return &o, fmt.Errorf("event and objective Ids do not match: %s and %s respectively", string(event.ObjectiveId), string(o.Id()))
	}

	updated := o.clone()
	updated.C.AddSignedStates(event.SignedStates)

	if event.Holdings != nil {
		updated.C.OnChainFunding = event.Holdings
	}

	return &updated, nil
}

// Crank inspects the extended state and declares a list of Effects to be executed
// It's like a state machine transition function where the finite / enumerable state is returned (computed from the extended state)
// rather than being independent of the extended state; and where there is only one type of event ("the crank") with no data on it at all
func (o Objective) Crank(secretKey *[]byte) (protocols.Objective, protocols.SideEffects, protocols.WaitingFor, error) {
	updated := o.clone()

	sideEffects := protocols.SideEffects{}
	// Input validation
	if updated.Status != protocols.Approved {
		return &updated, protocols.SideEffects{}, WaitingForNothing, ErrNotApproved
	}

	// Prefunding
	if !updated.C.PreFundSignedByMe() {
		ss, err := updated.C.SignAndAddPrefund(secretKey)
		if err != nil {
			return &updated, protocols.SideEffects{}, WaitingForCompletePrefund, fmt.Errorf("could not sign prefund %w", err)
		}
		messages := protocols.CreateSignedStateMessages(updated.Id(), ss, updated.C.MyIndex)
		sideEffects.MessagesToSend = append(sideEffects.MessagesToSend, messages...)
	}

	if !updated.C.PreFundComplete() {
		return &updated, sideEffects, WaitingForCompletePrefund, nil
	}

	// Funding
	fundingComplete := updated.fundingComplete() // note all information stored in state (since there are no real events)
	amountToDeposit := updated.amountToDeposit()
	safeToDeposit := updated.safeToDeposit()

	if !fundingComplete && !safeToDeposit {
		return &updated, sideEffects, WaitingForMyTurnToFund, nil
	}

	if !fundingComplete && safeToDeposit && amountToDeposit.IsNonZero() {
		deposit := protocols.ChainTransaction{ChannelId: updated.C.Id, Deposit: amountToDeposit}
		sideEffects.TransactionsToSubmit = append(sideEffects.TransactionsToSubmit, deposit)
	}

	if !fundingComplete {
		return &updated, sideEffects, WaitingForCompleteFunding, nil
	}

	// Postfunding
	if !updated.C.PostFundSignedByMe() {

		ss, err := updated.C.SignAndAddPostfund(secretKey)

		if err != nil {
			return &updated, protocols.SideEffects{}, WaitingForCompletePostFund, fmt.Errorf("could not sign postfund %w", err)
		}
		messages := protocols.CreateSignedStateMessages(updated.Id(), ss, updated.C.MyIndex)
		sideEffects.MessagesToSend = append(sideEffects.MessagesToSend, messages...)
	}

	if !updated.C.PostFundComplete() {
		return &updated, sideEffects, WaitingForCompletePostFund, nil
	}

	// Completion
	return &updated, sideEffects, WaitingForNothing, nil
}

func (o Objective) Channels() []*channel.Channel {
	ret := make([]*channel.Channel, 0, 1)
	ret = append(ret, o.C)
	return ret
}

// MarshalJSON returns a JSON representation of the DirectFundObjective
//
// NOTE: Marshal -> Unmarshal is a lossy process. All channel data
//       (other than Id) from the field C is discarded
func (o Objective) MarshalJSON() ([]byte, error) {
	jsonDFO := jsonObjective{
		o.Status,
		o.C.Id,
		o.myDepositSafetyThreshold,
		o.myDepositTarget,
		o.fullyFundedThreshold,
	}
	return json.Marshal(jsonDFO)
}

// UnmarshalJSON populates the calling DirectFundObjective with the
// json-encoded data
//
// NOTE: Marshal -> Unmarshal is a lossy process. All channel data
//       (other than Id) from the field C is discarded
func (o *Objective) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}

	var jsonDFO jsonObjective
	err := json.Unmarshal(data, &jsonDFO)

	if err != nil {
		return err
	}

	o.C = &channel.Channel{}
	o.C.Id = jsonDFO.C

	o.Status = jsonDFO.Status
	o.fullyFundedThreshold = jsonDFO.FullyFundedThreshold
	o.myDepositTarget = jsonDFO.MyDepositTarget
	o.myDepositSafetyThreshold = jsonDFO.MyDepositSafetyThreshold

	return nil
}

//  Private methods on the DirectFundingObjectiveState

// fundingComplete returns true if the recorded OnChainHoldings are greater than or equal to the threshold for being fully funded.
func (o Objective) fundingComplete() bool {
	for asset, threshold := range o.fullyFundedThreshold {
		chainHolding, ok := o.C.OnChainFunding[asset]

		if !ok {
			return false
		}

		if types.Gt(threshold, chainHolding) {
			return false
		}
	}

	return true
}

// safeToDeposit returns true if the recorded OnChainHoldings are greater than or equal to the threshold for safety.
func (o Objective) safeToDeposit() bool {
	for asset, safetyThreshold := range o.myDepositSafetyThreshold {

		chainHolding, ok := o.C.OnChainFunding[asset]

		if !ok {
			panic("nil chainHolding for asset in myDepositSafetyThreshold")
		}

		if types.Gt(safetyThreshold, chainHolding) {
			return false
		}
	}

	return true
}

// amountToDeposit computes the appropriate amount to deposit given the current recorded OnChainHoldings
func (o Objective) amountToDeposit() types.Funds {
	deposits := make(types.Funds, len(o.C.OnChainFunding))

	for asset, target := range o.myDepositTarget {
		holding, ok := o.C.OnChainFunding[asset]
		if !ok {
			panic("nil chainHolding for asset in myDepositTarget")
		}
		deposits[asset] = big.NewInt(0).Sub(target, holding)
	}

	return deposits
}

// Equal returns true if the supplied Objective is deeply equal to the receiver.
func (o Objective) Equal(r Objective) bool {
	return o.Status == r.Status &&
		o.C.Equal(*r.C) &&
		o.myDepositSafetyThreshold.Equal(r.myDepositSafetyThreshold) &&
		o.myDepositTarget.Equal((r.myDepositTarget)) &&
		o.fullyFundedThreshold.Equal(r.fullyFundedThreshold)
}

// clone returns a deep copy of the receiver.
func (o Objective) clone() Objective {
	clone := Objective{}
	clone.Status = o.Status

	cClone := o.C.Clone()
	clone.C = cClone

	clone.myDepositSafetyThreshold = o.myDepositSafetyThreshold.Clone()
	clone.myDepositTarget = o.myDepositTarget.Clone()
	clone.fullyFundedThreshold = o.fullyFundedThreshold.Clone()

	return clone
}

// IsDirectFundObjective inspects a objective id and returns true if the objective id is for a direct fund objective.
func IsDirectFundObjective(id protocols.ObjectiveId) bool {
	return strings.HasPrefix(string(id), ObjectivePrefix)
}

// ConstructObjectiveFromMessage takes in a message and constructs a direct funding objective from it.
func ConstructObjectiveFromMessage(m protocols.Message, myAddress types.Address) (Objective, error) {

	if len(m.SignedStates) == 0 {
		return Objective{}, errors.New("expected at least one signed state in the message")
	}
	initialState := m.SignedStates[0].State()

	objective, err := NewObjective(
		true, // TODO ensure objective in only approved if the application has given permission somehow
		initialState,
		myAddress,
	)
	if err != nil {
		return Objective{}, fmt.Errorf("could not create new objective: %w", err)
	}
	return objective, nil
}

// mermaid diagram
// key:
// - effect!
// - waiting...
//
// https://mermaid-js.github.io/mermaid-live-editor/edit/#eyJjb2RlIjoiZ3JhcGggVERcbiAgICBTdGFydCAtLT4gQ3tJbnZhbGlkIElucHV0P31cbiAgICBDIC0tPnxZZXN8IEVbZXJyb3JdXG4gICAgQyAtLT58Tm98IEQwXG4gICAgXG4gICAgRDB7U2hvdWxkU2lnblByZUZ1bmR9XG4gICAgRDAgLS0-fFllc3wgUjFbU2lnblByZWZ1bmQhXVxuICAgIEQwIC0tPnxOb3wgRDFcbiAgICBcbiAgICBEMXtTYWZlVG9EZXBvc2l0ICY8YnI-ICFGdW5kaW5nQ29tcGxldGV9XG4gICAgRDEgLS0-IHxZZXN8IFIyW0Z1bmQgb24gY2hhaW4hXVxuICAgIEQxIC0tPiB8Tm98IEQyXG4gICAgXG4gICAgRDJ7IVNhZmVUb0RlcG9zaXQgJjxicj4gIUZ1bmRpbmdDb21wbGV0ZX1cbiAgICBEMiAtLT4gfFllc3wgUjNbXCJteSB0dXJuLi4uXCJdXG4gICAgRDIgLS0-IHxOb3wgRDNcblxuICAgIEQze1NhZmVUb0RlcG9zaXQgJjxicj4gIUZ1bmRpbmdDb21wbGV0ZX1cbiAgICBEMyAtLT4gfFllc3wgUjRbRGVwb3NpdCFdXG4gICAgRDMgLS0-IHxOb3wgRDRcblxuICAgIEQ0eyFGdW5kaW5nQ29tcGxldGV9XG4gICAgRDQgLS0-IHxZZXN8IFI1W1wiY29tcGxldGUgZnVuZGluZy4uLlwiXVxuICAgIEQ0IC0tPiB8Tm98IEQ1XG5cbiAgICBENXtTaG91bGRTaWduUHJlRnVuZH1cbiAgICBENSAtLT58WWVzfCBSNltTaWduUG9zdGZ1bmQhXVxuICAgIEQ1IC0tPnxOb3wgRDZcblxuICAgIEQ2eyFQb3N0RnVuZENvbXBsZXRlfVxuICAgIEQ2IC0tPnxZZXN8IFI3W1wiY29tcGxldGUgcG9zdGZ1bmQuLi5cIl1cbiAgICBENiAtLT58Tm98IFI4XG5cbiAgICBSOFtcImZpbmlzaFwiXVxuICAgIFxuXG5cbiIsIm1lcm1haWQiOiJ7fSIsInVwZGF0ZUVkaXRvciI6ZmFsc2UsImF1dG9TeW5jIjp0cnVlLCJ1cGRhdGVEaWFncmFtIjp0cnVlfQ
// graph TD
//     Start --> C{Invalid Input?}
//     C -->|Yes| E[error]
//     C -->|No| D0

//     D0{ShouldSignPreFund}
//     D0 -->|Yes| R1[SignPrefund!]
//     D0 -->|No| D1

//     D1{SafeToDeposit &<br> !FundingComplete}
//     D1 --> |Yes| R2[Fund on chain!]
//     D1 --> |No| D2

//     D2{!SafeToDeposit &<br> !FundingComplete}
//     D2 --> |Yes| R3["wait my turn..."]
//     D2 --> |No| D3

//     D3{SafeToDeposit &<br> !FundingComplete}
//     D3 --> |Yes| R4[Deposit!]
//     D3 --> |No| D4

//     D4{!FundingComplete}
//     D4 --> |Yes| R5["wait for complete funding..."]
//     D4 --> |No| D5

//     D5{ShouldSignPostFund}
//     D5 -->|Yes| R6[SignPostfund!]
//     D5 -->|No| D6

//     D6{!PostFundComplete}
//     D6 -->|Yes| R7["wait for complete postfund..."]
//     D6 -->|No| R8

//     R8["finish"]
