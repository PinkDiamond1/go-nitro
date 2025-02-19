package protocols

import (
	"encoding/json"
	"math/big"
	"reflect"
	"testing"

	"github.com/statechannels/go-nitro/channel/consensus_channel"
	"github.com/statechannels/go-nitro/channel/state"
	"github.com/statechannels/go-nitro/payments"
	"github.com/statechannels/go-nitro/types"
)

func removeProposal() consensus_channel.SignedProposal {
	remove := consensus_channel.NewRemoveProposal(types.Destination{'l'}, types.Destination{'a'}, big.NewInt(1))
	return consensus_channel.SignedProposal{Proposal: remove, Signature: state.Signature{}}
}

func addProposal() consensus_channel.SignedProposal {
	amount := big.NewInt(1)
	add := consensus_channel.NewAddProposal(types.Destination{'l'}, consensus_channel.NewGuarantee(
		amount,
		types.Destination{'a'},
		types.Destination{'b'},
		types.Destination{'c'},
	),
		amount,
	)

	return consensus_channel.SignedProposal{Proposal: add, Signature: state.Signature{}}
}

func toPayload(p interface{}) []byte {

	b, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return b
}

func TestMessage(t *testing.T) {
	ss := state.NewSignedState(state.TestState)
	msg := Message{
		To: types.Address{'a'},
		ObjectivePayloads: []ObjectivePayload{{
			ObjectiveId: `say-hello-to-my-little-friend`,
			PayloadData: toPayload(&ss),
		}},
		LedgerProposals:    []consensus_channel.SignedProposal{addProposal(), removeProposal()},
		Payments:           []payments.Voucher{{ChannelId: types.Destination{'d'}, Amount: big.NewInt(123), Signature: state.Signature{}}},
		RejectedObjectives: []ObjectiveId{"say-hello-to-my-little-friend2"},
	}

	msgString :=
		`{"To":"0x6100000000000000000000000000000000000000","ObjectivePayloads":[{"PayloadData":"eyJTdGF0ZSI6eyJDaGFpbklkIjo5MDAxLCJQYXJ0aWNpcGFudHMiOlsiMHhmNWExYmI1NjA3YzlkMDc5ZTQ2ZDFiM2RjMzNmMjU3ZDkzN2I0M2JkIiwiMHg3NjBiZjI3Y2Q0NTAzNmE2YzQ4NjgwMmQzMGI1ZDkwY2ZmYmUzMWZlIl0sIkNoYW5uZWxOb25jZSI6MzcxNDA2NzY1ODAsIkFwcERlZmluaXRpb24iOiIweDVlMjllNWFiOGVmMzNmMDUwYzdjYzEwYjVhMDQ1NmQ5NzVjNWY4OGQiLCJDaGFsbGVuZ2VEdXJhdGlvbiI6NjAsIkFwcERhdGEiOiIiLCJPdXRjb21lIjpbeyJBc3NldCI6IjB4MDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMCIsIk1ldGFkYXRhIjpudWxsLCJBbGxvY2F0aW9ucyI6W3siRGVzdGluYXRpb24iOiIweDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMGY1YTFiYjU2MDdjOWQwNzllNDZkMWIzZGMzM2YyNTdkOTM3YjQzYmQiLCJBbW91bnQiOjUsIkFsbG9jYXRpb25UeXBlIjowLCJNZXRhZGF0YSI6bnVsbH0seyJEZXN0aW5hdGlvbiI6IjB4MDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwZWUxOGZmMTU3NTA1NTY5MTAwOWFhMjQ2YWU2MDgxMzJjNTdhNDIyYyIsIkFtb3VudCI6NSwiQWxsb2NhdGlvblR5cGUiOjAsIk1ldGFkYXRhIjpudWxsfV19XSwiVHVybk51bSI6NSwiSXNGaW5hbCI6ZmFsc2V9LCJTaWdzIjp7fX0=","ObjectiveId":"say-hello-to-my-little-friend","Type":""}],"LedgerProposals":[{"R":null,"S":null,"V":0,"Proposal":{"LedgerID":"0x6c00000000000000000000000000000000000000000000000000000000000000","ToAdd":{"Guarantee":{"Amount":1,"Target":"0x6100000000000000000000000000000000000000000000000000000000000000","Left":"0x6200000000000000000000000000000000000000000000000000000000000000","Right":"0x6300000000000000000000000000000000000000000000000000000000000000"},"LeftDeposit":1},"ToRemove":{"Target":"0x0000000000000000000000000000000000000000000000000000000000000000","LeftAmount":null}},"TurnNum":0},{"R":null,"S":null,"V":0,"Proposal":{"LedgerID":"0x6c00000000000000000000000000000000000000000000000000000000000000","ToAdd":{"Guarantee":{"Amount":null,"Target":"0x0000000000000000000000000000000000000000000000000000000000000000","Left":"0x0000000000000000000000000000000000000000000000000000000000000000","Right":"0x0000000000000000000000000000000000000000000000000000000000000000"},"LeftDeposit":null},"ToRemove":{"Target":"0x6100000000000000000000000000000000000000000000000000000000000000","LeftAmount":1}},"TurnNum":0}],"Payments":[{"ChannelId":"0x6400000000000000000000000000000000000000000000000000000000000000","Amount":123,"Signature":{"R":null,"S":null,"V":0}}],"RejectedObjectives":["say-hello-to-my-little-friend2"]}`

	t.Run(`serialize`, func(t *testing.T) {
		got, err := msg.Serialize()
		if err != nil {
			t.Error(err)
		}
		want := msgString
		if got != want {
			t.Fatalf("incorrect serialization: got:\n%v\nwanted:\n%v", got, want)
		}
	})

	t.Run(`deserialize`, func(t *testing.T) {
		got, err := DeserializeMessage(msgString)
		want := msg
		if err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("incorrect deserialization: got:\n%v\nwanted:\n%v", got, want)
		}
	})

}
