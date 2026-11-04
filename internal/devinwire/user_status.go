package devinwire

import (
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// UserStatusPath is the seat-management RPC NeoPi's Devin usage calls.
const UserStatusPath = "/exa.seat_management_pb.SeatManagementService/GetUserStatus"

// BillingStrategyQuota marks a plan billed by daily/weekly quota.
const BillingStrategyQuota int32 = 2

// EncodeUserStatusRequest is GetUserStatusRequest{metadata}.
func EncodeUserStatusRequest(metadata Metadata) []byte {
	return appendMessage(nil, 1, metadata.encode())
}

// DevinPlanInfo is exa.codeium_common_pb.DevinPlanInfo's organization fields.
type DevinPlanInfo struct{ OrgID, AccountDisplayName string }

// PlanInfo is the exa.codeium_common_pb.PlanInfo fields NeoPi's usage reads.
type PlanInfo struct {
	TeamsTier                       int32
	PlanName                        string
	MonthlyPromptCredits            int32
	MonthlyFlowCredits              int32
	MonthlyFlexCreditPurchaseAmount int32
	Devin                           *DevinPlanInfo
	BillingStrategy                 int32
	HideDailyQuota, HideWeeklyQuota bool
}

// PlanStatus is the exa.codeium_common_pb.PlanStatus fields NeoPi's usage
// reads. Absent timestamps are zero.
type PlanStatus struct {
	PlanInfo                                            *PlanInfo
	PlanStart, PlanEnd                                  time.Time
	AvailablePromptCredits, AvailableFlowCredits        int32
	AvailableFlexCredits                                int32
	UsedPromptCredits, UsedFlowCredits, UsedFlexCredits int32
	DailyQuotaRemainingPercent                          int32
	WeeklyQuotaRemainingPercent                         int32
	OverageBalanceMicros                                int64
	DailyQuotaResetAtUnix, WeeklyQuotaResetAtUnix       int64
}

// UserStatus is the exa.codeium_common_pb.UserStatus fields NeoPi's usage reads.
type UserStatus struct {
	TeamID, Email, UserID string
	TeamsTier             int32
	PlanStatus            *PlanStatus
}

// UserStatusResponse is GetUserStatusResponse.
type UserStatusResponse struct {
	UserStatus *UserStatus
	PlanInfo   *PlanInfo
}

// DecodeUserStatus decodes a GetUserStatus response.
func DecodeUserStatus(b []byte) (UserStatusResponse, error) {
	var out UserStatusResponse
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
		if typ != protowire.BytesType {
			return nil
		}
		switch num {
		case 1:
			status, err := decodeUserStatusMessage(value)
			if err != nil {
				return err
			}
			out.UserStatus = &status
		case 2:
			plan, err := decodePlanInfo(value)
			if err != nil {
				return err
			}
			out.PlanInfo = &plan
		}
		return nil
	})
	return out, err
}

func decodeUserStatusMessage(b []byte) (UserStatus, error) {
	var out UserStatus
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		switch {
		case typ == protowire.BytesType && num == 5:
			out.TeamID = string(value)
		case typ == protowire.BytesType && num == 7:
			out.Email = string(value)
		case typ == protowire.VarintType && num == 10:
			out.TeamsTier = int32(varint)
		case typ == protowire.BytesType && num == 13:
			plan, err := decodePlanStatus(value)
			if err != nil {
				return err
			}
			out.PlanStatus = &plan
		case typ == protowire.BytesType && num == 36:
			out.UserID = string(value)
		}
		return nil
	})
	return out, err
}

func decodePlanStatus(b []byte) (PlanStatus, error) {
	var out PlanStatus
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		if typ == protowire.BytesType {
			switch num {
			case 1:
				plan, err := decodePlanInfo(value)
				if err != nil {
					return err
				}
				out.PlanInfo = &plan
			case 2, 3:
				stamp, err := decodeTimestamp(value)
				if err != nil {
					return err
				}
				if num == 2 {
					out.PlanStart = stamp
				} else {
					out.PlanEnd = stamp
				}
			}
			return nil
		}
		if typ != protowire.VarintType {
			return nil
		}
		switch num {
		case 4:
			out.AvailableFlexCredits = int32(varint)
		case 5:
			out.UsedFlowCredits = int32(varint)
		case 6:
			out.UsedPromptCredits = int32(varint)
		case 7:
			out.UsedFlexCredits = int32(varint)
		case 8:
			out.AvailablePromptCredits = int32(varint)
		case 9:
			out.AvailableFlowCredits = int32(varint)
		case 14:
			out.DailyQuotaRemainingPercent = int32(varint)
		case 15:
			out.WeeklyQuotaRemainingPercent = int32(varint)
		case 16:
			out.OverageBalanceMicros = int64(varint)
		case 17:
			out.DailyQuotaResetAtUnix = int64(varint)
		case 18:
			out.WeeklyQuotaResetAtUnix = int64(varint)
		}
		return nil
	})
	return out, err
}

func decodePlanInfo(b []byte) (PlanInfo, error) {
	var out PlanInfo
	err := fields(b, func(num protowire.Number, typ protowire.Type, value []byte, varint uint64) error {
		switch {
		case typ == protowire.VarintType && num == 1:
			out.TeamsTier = int32(varint)
		case typ == protowire.BytesType && num == 2:
			out.PlanName = string(value)
		case typ == protowire.VarintType && num == 12:
			out.MonthlyPromptCredits = int32(varint)
		case typ == protowire.VarintType && num == 13:
			out.MonthlyFlowCredits = int32(varint)
		case typ == protowire.VarintType && num == 14:
			out.MonthlyFlexCreditPurchaseAmount = int32(varint)
		case typ == protowire.BytesType && num == 33:
			devin := DevinPlanInfo{}
			if err := fields(value, func(num protowire.Number, typ protowire.Type, value []byte, _ uint64) error {
				if typ == protowire.BytesType && num == 4 {
					devin.OrgID = string(value)
				} else if typ == protowire.BytesType && num == 8 {
					devin.AccountDisplayName = string(value)
				}
				return nil
			}); err != nil {
				return err
			}
			out.Devin = &devin
		case typ == protowire.VarintType && num == 35:
			out.BillingStrategy = int32(varint)
		case typ == protowire.VarintType && num == 36:
			out.HideDailyQuota = varint != 0
		case typ == protowire.VarintType && num == 37:
			out.HideWeeklyQuota = varint != 0
		}
		return nil
	})
	return out, err
}

// decodeTimestamp is google.protobuf.Timestamp; a message that is present
// but empty is the epoch, as protobuf-es reads it.
func decodeTimestamp(b []byte) (time.Time, error) {
	var seconds int64
	var nanos int32
	err := fields(b, func(num protowire.Number, typ protowire.Type, _ []byte, varint uint64) error {
		if typ == protowire.VarintType && num == 1 {
			seconds = int64(varint)
		} else if typ == protowire.VarintType && num == 2 {
			nanos = int32(varint)
		}
		return nil
	})
	return time.Unix(seconds, int64(nanos)).UTC(), err
}
