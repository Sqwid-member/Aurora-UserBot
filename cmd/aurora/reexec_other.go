//go:build !unix

package main

import "errors"

// reexecSelf is unsupported off Unix: the user restarts the menu by hand.
func reexecSelf() error {
	return errors.New("автоперезапуск меню не підтримується на цій платформі — запустіть aurora вручну")
}
