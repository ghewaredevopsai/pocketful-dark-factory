package app

// POST /payments/{payment_id}/refunds — the ninth idempotent write path (stage 4).
//
// A refund is a new payment from the original receiver back to the original sender, linked
// by refund_of, copying the target's note and visibility. Check order (after the
// idempotency wrapper: 401 → key header → body object → claimed key): 404 unknown → 403
// not the original receiver → 422 amount → 422 invalid_refund_target (the target is a
// refund) → 422 refund_exceeds_payment (all refunds would exceed the target's current
// corrected amount) → 409 insufficient_funds (the receiver's available funds).
func (s *Server) postRefund(st *State, uid string, m map[string]any, params []string) (int, any, *apiErr) {
	p := st.paymentByID[params[0]]
	if p == nil {
		return 0, nil, notFound("no such payment")
	}
	if p.To != uid {
		return 0, nil, forbidden("only the original receiver may refund a payment")
	}
	amount, e := amountField(m, "amount")
	if e != nil {
		return 0, nil, e
	}
	if p.RefundOf != nil {
		return 0, nil, errf(422, "invalid_refund_target", "a refund cannot be refunded")
	}
	if st.refunded[p.ID]+amount > latestRev(p).Amount {
		return 0, nil, errf(422, "refund_exceeds_payment", "refunds would exceed the payment's current amount")
	}
	receiver, sender := st.userByID[p.To], st.userByID[p.From]
	if st.available(receiver) < amount {
		return 0, nil, errf(409, "insufficient_funds", "available balance is below amount")
	}
	if sender.Balance > maxBalance-amount {
		return 0, nil, invalid("the refund pushes a wallet above 2^53")
	}
	at, _ := st.stamp()
	r := st.transfer(receiver, sender, amount, p.Note, p.Visibility, nil, nil, at)
	id := p.ID
	r.RefundOf = &id
	st.refunded[p.ID] += amount
	return 201, st.paymentView(r), nil
}
