// Copyright (C) 2019 Storj Labs, Inc.
// See LICENSE for copying information.

package payments

import (
	"context"
	"time"

	"storj.io/common/memory"
	"storj.io/common/uuid"
)

// Coupons exposes all needed functionality to manage coupons.
//
// architecture: Service
type Coupons interface {
	// GetByUserID returns the coupon applied to the specified user.
	GetByUserID(ctx context.Context, userID uuid.UUID) (*Coupon, error)

	// ListByUserID return list of all coupons of specified payment account.
	ListByUserID(ctx context.Context, userID uuid.UUID) ([]CouponOld, error)

	// TotalUsage returns sum of all usage records for specified coupon.
	TotalUsage(ctx context.Context, couponID uuid.UUID) (int64, error)

	// Create attaches a coupon for payment account.
	Create(ctx context.Context, coupon CouponOld) (coup CouponOld, err error)

	// AddPromotionalCoupon is used to add a promotional coupon for specified users who already have
	// a project and do not have a promotional coupon yet.
	// And updates project limits to selected size.
	AddPromotionalCoupon(ctx context.Context, userID uuid.UUID) error

	// PopulatePromotionalCoupons is used to populate promotional coupons through all active users who already have
	// a project, payment method and do not have a promotional coupon yet.
	// And updates project limits to selected size.
	PopulatePromotionalCoupons(ctx context.Context, duration *int, amount int64, projectLimit memory.Size) error

	// ApplyCouponCode attempts to apply a coupon code to the user.
	ApplyCouponCode(ctx context.Context, userID uuid.UUID, couponCode string) (*Coupon, error)
}

// Coupon describes a discount to the payment account of a user.
type Coupon struct {
	ID         string         `json:"id"`
	PromoCode  string         `json:"promoCode"`
	Name       string         `json:"name"`
	AmountOff  int64          `json:"amountOff"`
	PercentOff float64        `json:"percentOff"`
	AddedAt    time.Time      `json:"addedAt"`
	ExpiresAt  time.Time      `json:"expiresAt"`
	Duration   CouponDuration `json:"duration"`
}

// CouponDuration represents how many billing periods a coupon is applied.
type CouponDuration string

const (
	// CouponOnce indicates that a coupon can only be applied once.
	CouponOnce CouponDuration = "once"
	// CouponRepeating indicates that a coupon is applied every billing period for a definite amount of time.
	CouponRepeating = "repeating"
	// CouponForever indicates that a coupon is applied every billing period forever.
	CouponForever = "forever"
)

// CouponOld is an entity that adds some funds to Accounts balance for some fixed period.
// CouponOld is attached to the project.
// At the end of the period, the entire remaining coupon amount will be returned from the account balance.
// Deprecated: Use Coupon instead.
// TODO: This struct should be removed with the rest of the custom coupon implementation code.
type CouponOld struct {
	ID          uuid.UUID    `json:"id"`
	UserID      uuid.UUID    `json:"userId"`
	Amount      int64        `json:"amount"`   // Amount is stored in cents.
	Duration    *int         `json:"duration"` // Duration is stored in number of billing periods.
	Description string       `json:"description"`
	Type        CouponType   `json:"type"`
	Status      CouponStatus `json:"status"`
	Created     time.Time    `json:"created"`
}

// ExpirationDate returns coupon expiration date.
//
// A coupon is valid for Duration number of full months. The month the user
// signs up is not counted in the duration. The expirated date is at the last
// day of the last valid month.
func (coupon *CouponOld) ExpirationDate() *time.Time {
	if coupon.Duration == nil {
		return nil
	}

	expireDate := time.Date(coupon.Created.Year(), coupon.Created.Month()+time.Month(*coupon.Duration)+1, 0, 0, 0, 0, 0, time.UTC)
	return &expireDate
}

// CouponType indicates the type of the coupon.
type CouponType int

const (
	// CouponTypePromotional defines that this coupon is a promotional coupon.
	CouponTypePromotional CouponType = 0
)

// CouponStatus indicates the state of the coupon.
type CouponStatus int

const (
	// CouponActive is a default coupon state.
	CouponActive CouponStatus = 0
	// CouponUsed status indicates that coupon was used.
	CouponUsed CouponStatus = 1
	// CouponExpired status indicates that coupon is expired and unavailable.
	CouponExpired CouponStatus = 2
)

// CouponsPage holds set of coupon and indicates if
// there are more coupons to fetch.
type CouponsPage struct {
	Coupons    []CouponOld
	Next       bool
	NextOffset int64
}
