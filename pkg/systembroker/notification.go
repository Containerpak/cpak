/*
 * Copyright (c) 2025 FABRICATORS S.R.L.
 * SPDX-License-Identifier: GPL-3.0-only
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

func sendNotification(ctx context.Context, request NotificationRequest) error {
	hints := map[string]dbus.Variant{}
	if request.Urgency != "" {
		urgency := map[string]byte{"low": 0, "normal": 1, "critical": 2}
		hints["urgency"] = dbus.MakeVariant(urgency[request.Urgency])
	}
	if request.Category != "" {
		hints["category"] = dbus.MakeVariant(request.Category)
	}
	if request.Transient {
		hints["transient"] = dbus.MakeVariant(true)
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
		request.AppName,
		request.ReplaceID,
		request.Icon,
		request.Summary,
		request.Body,
		[]string{},
		hints,
		request.ExpireTimeout,
	)
	var notificationID uint32
	return call.Store(&notificationID)
}

func parseNotification(args []string) (NotificationRequest, error) {
	request := NotificationRequest{AppName: "cpak", ExpireTimeout: -1}
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
				return NotificationRequest{}, err
			}
			index = next
			urgency := map[string]bool{"low": true, "normal": true, "critical": true}
			_, ok := urgency[value]
			if !ok {
				return NotificationRequest{}, errors.New("invalid notification urgency")
			}
			request.Urgency = value
		case "-t", "--expire-time":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return NotificationRequest{}, err
			}
			index = next
			expire, err := strconv.ParseInt(value, 10, 32)
			if err != nil || expire < -1 {
				return NotificationRequest{}, errors.New("invalid notification expiry")
			}
			request.ExpireTimeout = int32(expire)
		case "-a", "--app-name":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return NotificationRequest{}, err
			}
			index = next
			request.AppName = value
		case "-i", "--icon":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return NotificationRequest{}, err
			}
			index = next
			request.Icon = value
		case "-c", "--category":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return NotificationRequest{}, err
			}
			index = next
			request.Category = value
		case "-r", "--replace-id":
			value, next, err := notificationOptionValue(args, index, inline, hasInline)
			if err != nil {
				return NotificationRequest{}, err
			}
			index = next
			replacement, err := strconv.ParseUint(value, 10, 32)
			if err != nil {
				return NotificationRequest{}, errors.New("invalid notification replacement ID")
			}
			request.ReplaceID = uint32(replacement)
		case "--transient":
			request.Transient = true
		default:
			if strings.HasPrefix(argument, "-") {
				return NotificationRequest{}, fmt.Errorf("unsupported notification option: %s", name)
			}
			positionals = append(positionals, argument)
		}
	}
	if len(positionals) < 1 || len(positionals) > 2 {
		return NotificationRequest{}, errors.New("notification requires a summary and optional body")
	}
	request.Summary = positionals[0]
	if len(positionals) == 2 {
		request.Body = positionals[1]
	}
	if err := validateNotification(request); err != nil {
		return NotificationRequest{}, err
	}
	return request, nil
}

func validateNotification(request NotificationRequest) error {
	if request.AppName == "" || request.Summary == "" || request.ExpireTimeout < -1 {
		return errors.New("invalid notification request")
	}
	if request.Urgency != "" && request.Urgency != "low" && request.Urgency != "normal" && request.Urgency != "critical" {
		return errors.New("invalid notification urgency")
	}
	for _, value := range []string{request.AppName, request.Icon, request.Summary, request.Body, request.Category} {
		if len(value) > 4096 || strings.ContainsRune(value, '\x00') {
			return errors.New("invalid notification request")
		}
	}
	return nil
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
