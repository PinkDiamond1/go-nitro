// SPDX-License-Identifier: MIT
pragma solidity 0.8.17;
pragma experimental ABIEncoderV2;

import './interfaces/IForceMoveApp.sol';
import './libraries/NitroUtils.sol';
import './interfaces/INitroTypes.sol';
import {ExitFormat as Outcome} from '@statechannels/exit-format/contracts/ExitFormat.sol';

/**
 * @dev The VirtualPaymentApp contract complies with the ForceMoveApp interface and allows payments to be made virtually from Alice to Bob (participants[0] to participants[n+1], where n is the number of intermediaries).
 */
contract VirtualPaymentApp is IForceMoveApp {
    struct VoucherAmountAndSignature {
        uint256 amount;
        INitroTypes.Signature signature; // signature on abi.encode(channelId,amount)
    }

    enum AllocationIndices {
        Alice, // payer
        Bob // beneficiary, initial allocation is zero
    }

    /**
     * @notice Encodes application-specific rules for a particular ForceMove-compliant state channel.
     * @dev Encodes application-specific rules for a particular ForceMove-compliant state channel.
     * @param fixedPart Fixed part of the state channel.
     * @param proof Array of recovered variable parts which constitutes a support proof for the candidate.
     * @param candidate Recovered variable part the proof was supplied for.
     */
    function requireStateSupported(
        FixedPart calldata fixedPart,
        RecoveredVariablePart[] calldata proof,
        RecoveredVariablePart calldata candidate
    ) external pure override {
        // This channel has only 4 states which can be supported:
        // 0 prefund
        // 1 postfund
        // 2 redemption
        // 3 final

        // states 0,1,3 can be supported via unanimous consensus:

        if (proof.length == 0) {
            require(
                NitroUtils.getClaimedSignersNum(candidate.signedBy) ==
                    fixedPart.participants.length,
                '!unanimous; |proof|=0'
            );
            if (candidate.variablePart.turnNum == 0) return; // prefund
            if (candidate.variablePart.turnNum == 1) return; // postfund
            if (candidate.variablePart.turnNum == 3) {
                // final (note: there is a core protocol escape hatch for this, too, so it could be removed)
                require(candidate.variablePart.isFinal, '!final; turnNum=3 && |proof|=0');
                return;
            }
            revert('bad candidate turnNum; |proof|=0');
        }

        // State 2 can be supported via a forced transition from state 1:
        //
        //      (2)_B     [redemption state signed by Bob, includes a voucher signed by Alice. The outcome may be updated in favour of Bob]
        //      ^
        //      |
        //      (1)_AIB   [fully signed postfund]

        if (proof.length == 1) {
            requireProofOfUnanimousConsensusOnPostFund(proof[0], fixedPart.participants.length);
            require(candidate.variablePart.turnNum == 2, 'bad candidate turnNum; |proof|=1');
            require(
                NitroUtils.isClaimedSignedBy(candidate.signedBy, 2),
                'redemption not signed by Bob'
            );
            uint256 voucherAmount = requireValidVoucher(candidate.variablePart.appData, fixedPart);
            requireCorrectAdjustments(
                proof[0].variablePart.outcome,
                candidate.variablePart.outcome,
                voucherAmount
            );
            return;
        }
        revert('bad proof length');
    }

    function requireProofOfUnanimousConsensusOnPostFund(
        RecoveredVariablePart memory rVP,
        uint256 numParticipants
    ) internal pure {
        require(rVP.variablePart.turnNum == 1, 'bad proof[0].turnNum; |proof|=1');
        require(
            NitroUtils.getClaimedSignersNum(rVP.signedBy) == numParticipants,
            'postfund !unanimous; |proof|=1'
        );
    }

    function requireValidVoucher(bytes memory appData, FixedPart memory fixedPart)
        internal
        pure
        returns (uint256)
    {
        VoucherAmountAndSignature memory voucher = abi.decode(appData, (VoucherAmountAndSignature));

        address signer = NitroUtils.recoverSigner(
            keccak256(abi.encode(NitroUtils.getChannelId(fixedPart), voucher.amount)),
            voucher.signature
        );
        require(signer == fixedPart.participants[0], 'invalid signature for voucher'); // could be incorrect channelId or incorrect signature
        return voucher.amount;
    }

    function requireCorrectAdjustments(
        Outcome.SingleAssetExit[] memory oldOutcome,
        Outcome.SingleAssetExit[] memory newOutcome,
        uint256 voucherAmount
    ) internal pure {
        require(
            oldOutcome.length == 1 &&
                newOutcome.length == 1 &&
                oldOutcome[0].asset == address(0) &&
                newOutcome[0].asset == address(0),
            'only native asset allowed'
        );

        require(
            voucherAmount <= oldOutcome[0].allocations[uint256(AllocationIndices.Alice)].amount,
            'underflow'
        ); // TODO this check may be unecessary in a future version of solidity

        require(
            newOutcome[0].allocations[uint256(AllocationIndices.Alice)].amount ==
                oldOutcome[0].allocations[uint256(AllocationIndices.Alice)].amount - voucherAmount,
            'Alice not adjusted correctly'
        );
        require(
            newOutcome[0].allocations[uint256(AllocationIndices.Bob)].amount == voucherAmount,
            'Bob not adjusted correctly'
        );
    }
}
