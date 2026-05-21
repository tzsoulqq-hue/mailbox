package main

import (
	"regexp"
	"sort"
	"strings"

	"mailboxapi/pb"
)

const (
	emailParserProfileGeneric = "generic"
)

var (
	verificationCodePattern = regexp.MustCompile(`(?i)(?:verification|security|login|one[- ]?time|otp|code|验证码|安全代码)[^0-9]{0,80}([0-9]{4,8})`)
	standaloneCodePattern   = regexp.MustCompile(`(^|[^0-9])([0-9]{6})([^0-9]|$)`)
)

func emailMessageWithSignals(message *pb.EmailInboxMessage, profile string) *pb.EmailInboxMessage {
	if message == nil {
		return nil
	}
	message.Signals = parseEmailSignals(message, profile)
	message.PrimarySignal = primaryEmailSignal(message.Signals)
	return message
}

func parseEmailSignals(message *pb.EmailInboxMessage, profile string) []*pb.EmailSignal {
	if message == nil {
		return nil
	}
	return dedupeEmailSignals(parseGenericEmailSignals(message, normalizeParserProfile(profile)))
}

func parseGenericEmailSignals(message *pb.EmailInboxMessage, profile string) []*pb.EmailSignal {
	text := emailSignalText(message)
	code, evidence := extractVerificationCode(text)
	if code == "" {
		return nil
	}
	return []*pb.EmailSignal{{
		Kind:       pb.EmailSignalKind_EMAIL_SIGNAL_KIND_OTP,
		Code:       code,
		Label:      "验证码",
		Profile:    profile,
		Parser:     "generic-verification-code",
		Confidence: 70,
		Evidence:   evidence,
	}}
}

func extractVerificationCode(text string) (string, string) {
	if match := verificationCodePattern.FindStringSubmatch(text); len(match) >= 2 {
		return match[1], compactMessageText(match[0], 120)
	}
	if match := standaloneCodePattern.FindStringSubmatch(text); len(match) >= 3 {
		return match[2], strings.TrimSpace(match[0])
	}
	return "", ""
}

func emailSignalText(message *pb.EmailInboxMessage) string {
	return strings.Join([]string{
		message.GetSubject(),
		message.GetFromAddress(),
		message.GetBodyPreview(),
		message.GetBodyText(),
		compactMessageText(message.GetHtmlBody(), 5000),
	}, "\n")
}

func primaryEmailSignal(signals []*pb.EmailSignal) *pb.EmailSignal {
	if len(signals) == 0 {
		return nil
	}
	sorted := append([]*pb.EmailSignal{}, signals...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].GetKind() != sorted[j].GetKind() {
			return signalPriority(sorted[i].GetKind()) < signalPriority(sorted[j].GetKind())
		}
		return sorted[i].GetConfidence() > sorted[j].GetConfidence()
	})
	return sorted[0]
}

func signalPriority(kind pb.EmailSignalKind) int {
	switch kind {
	case pb.EmailSignalKind_EMAIL_SIGNAL_KIND_OTP:
		return 0
	default:
		return 9
	}
}

func messageHasSignal(message *pb.EmailInboxMessage, kind pb.EmailSignalKind) bool {
	if kind == pb.EmailSignalKind_EMAIL_SIGNAL_KIND_UNSPECIFIED {
		return true
	}
	for _, signal := range message.GetSignals() {
		if signal.GetKind() == kind {
			return true
		}
	}
	return false
}

func firstSignalCode(message *pb.EmailInboxMessage, kind pb.EmailSignalKind) string {
	if message == nil {
		return ""
	}
	if signal := message.GetPrimarySignal(); signal.GetKind() == kind && signal.GetCode() != "" {
		return signal.GetCode()
	}
	for _, signal := range message.GetSignals() {
		if signal.GetKind() == kind && signal.GetCode() != "" {
			return signal.GetCode()
		}
	}
	return ""
}

func dedupeEmailSignals(signals []*pb.EmailSignal) []*pb.EmailSignal {
	best := map[string]*pb.EmailSignal{}
	for _, signal := range signals {
		if signal == nil || signal.GetKind() == pb.EmailSignalKind_EMAIL_SIGNAL_KIND_UNSPECIFIED {
			continue
		}
		key := strings.Join([]string{
			signal.GetKind().String(),
			strings.TrimSpace(signal.GetCode()),
			strings.TrimSpace(signal.GetLabel()),
		}, "\x00")
		if existing, ok := best[key]; !ok || signal.GetConfidence() > existing.GetConfidence() {
			best[key] = signal
		}
	}
	out := make([]*pb.EmailSignal, 0, len(best))
	for _, signal := range best {
		out = append(out, signal)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].GetKind() != out[j].GetKind() {
			return signalPriority(out[i].GetKind()) < signalPriority(out[j].GetKind())
		}
		return out[i].GetConfidence() > out[j].GetConfidence()
	})
	return out
}

func normalizeParserProfile(profile string) string {
	return emailParserProfileGeneric
}
