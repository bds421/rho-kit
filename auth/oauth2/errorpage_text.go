package oauth2

import (
	"strconv"
	"strings"
)

type errorText struct {
	lang     string
	title    string
	action   string
	messages map[failure]string
}

var errorTexts = map[string]errorText{
	"en": {
		lang:   "en",
		title:  "Sign-in not completed",
		action: "Sign in again",
		messages: map[failure]string{
			failureSignIn:      "The sign-in could not be completed. Please sign in again.",
			failureRefused:     "This account cannot sign in. Please contact your administrator.",
			failureUnavailable: "Sign-in is temporarily unavailable. Please try again in a moment.",
		},
	},
	"de": {
		lang:   "de",
		title:  "Anmeldung nicht abgeschlossen",
		action: "Erneut anmelden",
		messages: map[failure]string{
			failureSignIn:      "Die Anmeldung konnte nicht abgeschlossen werden. Bitte melden Sie sich erneut an.",
			failureRefused:     "Mit diesem Konto ist keine Anmeldung möglich. Bitte wenden Sie sich an Ihre Administration.",
			failureUnavailable: "Die Anmeldung ist vorübergehend nicht verfügbar. Bitte versuchen Sie es gleich noch einmal.",
		},
	},
	"es": {
		lang:   "es",
		title:  "Inicio de sesión no completado",
		action: "Iniciar sesión de nuevo",
		messages: map[failure]string{
			failureSignIn:      "No se pudo completar el inicio de sesión. Por favor, inicie sesión de nuevo.",
			failureRefused:     "Esta cuenta no puede iniciar sesión. Por favor, póngase en contacto con su administrador.",
			failureUnavailable: "El inicio de sesión no está disponible temporalmente. Por favor, inténtelo de nuevo en un momento.",
		},
	},
}

// preferredLanguage picks the supported language the browser ranks highest
// in an Accept-Language header, English when none matches.
func preferredLanguage(header string) string {
	best, bestQ := "en", 0.0
	for _, part := range strings.Split(header, ",") {
		tag, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		primary, _, _ := strings.Cut(strings.ToLower(tag), "-")
		if _, ok := errorTexts[primary]; !ok {
			continue
		}
		q := 1.0
		if v, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			parsed, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			q = parsed
		}
		if q > bestQ {
			best, bestQ = primary, q
		}
	}
	return best
}
