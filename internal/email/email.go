package email

import (
	"context"
	"fmt"

	"github.com/resend/resend-go/v4"
)

type EmailSender interface {
	SendLoginCode(ctx context.Context, email, code string) error
}

type ResendEmailSender struct {
	client    *resend.Client
	fromEmail string
}

func NewResendEmailSender(apiKey, fromEmail string) *ResendEmailSender {
	return &ResendEmailSender{
		client:    resend.NewClient(apiKey),
		fromEmail: fromEmail,
	}
}

func (r *ResendEmailSender) SendLoginCode(ctx context.Context, email, code string) error {
	params := &resend.SendEmailRequest{
		From:    r.fromEmail,
		To:      []string{email},
		Subject: "Your Sudux Verification Code",
		Html:    fmt.Sprintf("<p>Your verification code for Sudux is: <strong>%s</strong></p><p>This code expires in 5 minutes.</p>", code),
	}

	_, err := r.client.Emails.SendWithContext(ctx, params)
	if err != nil {
		return fmt.Errorf("failed to send email via Resend: %w", err)
	}

	return nil
}

type DevEmailSender struct{}

func NewDevEmailSender() *DevEmailSender {
	return &DevEmailSender{}
}

func (d *DevEmailSender) SendLoginCode(ctx context.Context, email, code string) error {
	fmt.Printf("[DEV EMAIL SENDER] To: %s | Verification Code: %s\n", email, code)
	return nil
}
