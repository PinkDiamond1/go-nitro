package client

import (
	"log"
	"os"
	"testing"

	"github.com/statechannels/go-nitro/client/engine/chainservice"
	"github.com/statechannels/go-nitro/client/engine/messageservice"
	"github.com/statechannels/go-nitro/client/engine/store"
	"github.com/statechannels/go-nitro/crypto"
	"github.com/statechannels/go-nitro/types"
)

func TestNew(t *testing.T) {

	// Set up logging
	logDestination, err := os.OpenFile("client_test_logs.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	if err != nil {
		log.Fatal(err)
	}

	// Reset log destination file
	err = logDestination.Truncate(0)
	if err != nil {
		log.Fatal(err)
	}

	aKey, a := crypto.GeneratePrivateKeyAndAddress()
	bKey, b := crypto.GeneratePrivateKeyAndAddress()
	chain := chainservice.NewMockChain([]types.Address{a, b})

	chainservA := chainservice.NewSimpleChainService(chain, a)
	messageserviceA := messageservice.NewTestMessageService(a)
	storeA := store.NewMockStore(aKey)
	New(messageserviceA, chainservA, storeA, logDestination)

	chainservB := chainservice.NewSimpleChainService(chain, b)
	messageserviceB := messageservice.NewTestMessageService(b)
	storeB := store.NewMockStore(bKey)
	New(messageserviceB, chainservB, storeB, logDestination)

	messageserviceA.Connect(messageserviceB)
	messageserviceB.Connect(messageserviceA)
	// TODO: Temporarily disabled: See https://github.com/statechannels/go-nitro/issues/201
	// clientA.CreateDirectChannel(b, types.Address{}, types.Bytes{}, outcome.Exit{}, big.NewInt(0))

}
