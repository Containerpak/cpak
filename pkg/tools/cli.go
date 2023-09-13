package tools

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func ConfirmOperation(s string) bool {
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("%s [y/N]: ", s)
	text, _ := reader.ReadString('\n')
	text = strings.Replace(text, "\n", "", -1)
	return strings.ToLower(text) == "y"
}
