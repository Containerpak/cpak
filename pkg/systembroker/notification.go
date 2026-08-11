/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * Licensed under the Fabricators Public Access License (FPAL-TCV) v1.0.
 * See https://github.com/fabricatorsltd/FPAL/blob/main/LICENSE-TCV.md for details.
 */
package systembroker

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

type notification struct {
	appName       string
	replacesID    uint32
	icon          string
	summary       string
	body          string
	hints         map[string]dbus.Variant
	expireTimeout int32
}

func sendNotification(ctx context.Context, args []string) error {
	request, err := parseNotification(args)
	if err != nil {
		return err
	}
	connection, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer connection.Close()
	call := connection.Object("org.freedesktop.Notifications", "/org/freedesktop/Notifications").CallWithContext(
		ctx,
		"org.freedesktop.Notifications.Notify",
		0,
		request.appName,
		request.replacesID,
		request.icon,
		request.summary,
		request.body,
		[]string{},
		request.hints,
		request.expireTimeout,
	)
	var notificationID uint32
	return call.Store(&notificationID)
}

func parseNotification(args []string) (notification, error) {
	request := notification{appName: "cpak", hints: map[string]dbus.Variant{}, expireTimeout: -1}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			positionals = append(positionals, args[index+1:]...)
			break
		}
		name, inline, hasInline := strings.Cut(argument, "=")
		switch name {
		case "-u", "--urgency":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return notification{}, err
			}
			index = next
			urgency := map[string]byte{"low": 0, "normal": 1, "critical": 2}
			level, ok := urgency[value]
			if !ok {
				return notification{}, errors.New("invalid notification urgency")
			}
			request.hints["urgency"] = dbus.MakeVariant(level)
		case "-t", "--expire-time":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return notification{}, err
			}
			index = next
			expire, err := strconv.ParseInt(value, 10, 32)
			if err != nil || expire < -1 {
				return notification{}, errors.New("invalid notification expiry")
			}
			request.expireTimeout = int32(expire)
		case "-a", "--app-name":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return notification{}, err
			}
			index = next
			request.appName = value
		case "-i", "--icon":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return notification{}, err
			}
			index = next
			request.icon = value
		case "-c", "--category":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return notification{}, err
			}
			index = next
			request.hints["category"] = dbus.MakeVariant(value)
		case "-r", "--replace-id":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return notification{}, err
			}
			index = next
			replacement, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return notification{}, errors.New("invalid notification replacement ID")
			}
			request.replacesID = uint32(replacement)
		case "--transient":
			request.hints["transient"] = dbus.MakeVariant(true)
		default:
			if strings.HasPrefix(argument, "-") {
				return notification{}, fmt.Errorf("unsupported notification option: %s", name)
			}
			positionals = append(positionals, argument)
		}
	}
	if len(positionals) < 1 || len(positionals) > 2 {
		return notification{}, errors.New("notification requires a summary and optional body")
	}
	request.summary = positionals[0]
	if len(positionals) == 2 {
		request.body = positionals[1]
	}
	for _, value := range []string{request.appName, request.icon, request.summary, request.body} {
		if len(value) > 4096 || strings.ContainsRune(value, '\x00') {
			return notification{}, errors.New("invalid notification request")
		}
	}
	return request, nil
}

func notificationOptionValue(args []string, index int, inline string, hasInline bool) (string, int, error) {
	if hasInline {
		if inline == "" {
			return "", index, errors.New("notification option requires a value")
		}
		return inline, index, nil
	}
	if index+1 >= len(args) {
		return "", index, errors.New("notification option requires a value")
	}
	return args[index+1], index + 1, nil
}
