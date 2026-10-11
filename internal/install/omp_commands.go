package install

// addSlashExecutable records the absolute bicz executable in an OMP/OpenCode
// adapter config as "bicz_path", so the native /bicz command handler can run
// `bicz command` by argv without a shell or a PATH lookup. Resolution matches
// the hook entries (resolveExecutable). When the running binary is not a
// usable bicz executable (for example a test binary), the field is omitted
// and the plugin falls back to $BICZ_BIN, then `bicz` on PATH; this is never
// an install failure because tool metering does not depend on it.
func addSlashExecutable(fields map[string]any, opts Options) error {
	exe, err := resolveExecutable(opts)
	if err != nil {
		if opts.Executable != "" {
			// An explicit executable that is unusable is a real error.
			return err
		}
		return nil
	}
	fields["bicz_path"] = exe
	return nil
}
