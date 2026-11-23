package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/kballard/go-shellquote"
	"github.com/spf13/cobra"

	"github.com/20Youssef10/golab/internal/app"
	"github.com/20Youssef10/golab/internal/contents"
	"github.com/20Youssef10/golab/internal/platform"
)

func contentsForSession(ctx context.Context, d *app.Deps, name string) (contents.Client, string, error) {
	resolved, err := resolveSessionName(ctx, d, name)
	if err != nil {
		return nil, "", sessionErr(name, err)
	}
	s, err := d.GetSession(ctx, resolved)
	if err != nil {
		return nil, "", sessionErr(resolved, err)
	}
	c, err := d.ContentsFor(s)
	if err != nil {
		return nil, "", err
	}
	return c, resolved, nil
}

func lsCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "ls [PATH]",
		Short: "List files in a session",
		Args:  maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			remote := "content"
			if len(args) > 0 {
				remote = args[0]
			}
			c, session, err := contentsForSession(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			model, err := c.List(cmd.Context(), remote)
			if err != nil {
				return exitf(1, "[colab] Error: %s", err)
			}
			if model.Type != "directory" {
				fmt.Fprintln(d.Stdout, model.Name)
				return nil
			}
			entries := append([]contents.Model(nil), model.Entries...)
			sort.SliceStable(entries, func(i, j int) bool {
				di := entries[i].Type == "directory"
				dj := entries[j].Type == "directory"
				if di != dj {
					return di
				}
				return entries[i].Name < entries[j].Name
			})
			for _, e := range entries {
				suffix := ""
				if e.Type == "directory" {
					suffix = "/"
				}
				fmt.Fprintln(d.Stdout, e.Name+suffix)
			}
			writeEvent(d, session, "file_operation", map[string]any{"op": "ls", "path": remote})
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	return cmd
}

func rmCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "rm PATH",
		Short: "Remove a remote file",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			c, session, err := contentsForSession(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			if err := c.Delete(cmd.Context(), args[0]); err != nil {
				return exitf(1, "[colab] Error: %s", err)
			}
			writeEvent(d, session, "file_operation", map[string]any{"op": "rm", "path": args[0]})
			fmt.Fprintf(d.Stderr, "[colab] Deleted %s\n", args[0])
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	return cmd
}

func uploadCommand() *cobra.Command {
	var name string
	var recursive bool
	cmd := &cobra.Command{
		Use:   "upload LOCAL REMOTE",
		Short: "Upload a file or directory to a session",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			local, remote := args[0], args[1]
			info, err := os.Stat(local)
			if err != nil {
				label := "file"
				if recursive {
					label = "path"
				}
				return exitf(1, "[colab] Local %s '%s' not found.", label, local)
			}
			if info.IsDir() && !recursive {
				return exitf(1, "[colab] '%s' is a directory. Use -r/--recursive to upload directories.", local)
			}
			c, session, err := contentsForSession(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			if info.IsDir() {
				count, err := c.UploadDir(cmd.Context(), local, remote)
				if err != nil {
					return exitf(1, "[colab] Upload failed: %s", err)
				}
				writeEvent(d, session, "file_operation", map[string]any{
					"op": "upload", "local": local, "remote": remote,
					"recursive": true, "count": count,
				})
				fmt.Fprintf(d.Stderr, "[colab] Uploaded directory '%s' (%d file(s)) to '%s'\n", local, count, remote)
				return nil
			}
			f, err := os.Open(local)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			if err := c.WriteStream(cmd.Context(), remote, f, contents.DefaultChunkSize); err != nil {
				return exitf(1, "[colab] Upload failed: %s", err)
			}
			writeEvent(d, session, "file_operation", map[string]any{
				"op": "upload", "local": local, "remote": remote,
			})
			fmt.Fprintf(d.Stderr, "[colab] Uploaded '%s' to '%s'\n", local, remote)
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Recursively upload a directory")
	return cmd
}

func downloadCommand() *cobra.Command {
	var name string
	var recursive bool
	cmd := &cobra.Command{
		Use:   "download REMOTE LOCAL",
		Short: "Download a file or directory from a session",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			remote, local := args[0], args[1]
			c, session, err := contentsForSession(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			if recursive {
				count, err := c.DownloadDir(cmd.Context(), remote, local)
				if err != nil {
					return exitf(1, "[colab] Download failed: %s", err)
				}
				writeEvent(d, session, "file_operation", map[string]any{
					"op": "download", "remote": remote, "local": local,
					"recursive": true, "count": count,
				})
				fmt.Fprintf(d.Stderr, "[colab] Downloaded directory '%s' (%d file(s)) to '%s'\n", remote, count, local)
				return nil
			}
			dir := filepath.Dir(local)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return err
			}
			tmp, err := os.CreateTemp(dir, ".golab-download-*")
			if err != nil {
				return err
			}
			tmpPath := tmp.Name()
			defer func() { _ = os.Remove(tmpPath) }()
			_, err = c.ReadStream(cmd.Context(), remote, tmp)
			if closeErr := tmp.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
			if err != nil {
				if errors.Is(err, contents.ErrIsDirectory) {
					return exitf(1, "[colab] Download failed: %s. Use -r/--recursive to download directories.", err)
				}
				return exitf(1, "[colab] Download failed: %s", err)
			}
			if err := platform.MakePrivate(tmpPath); err != nil {
				return err
			}
			if runtime.GOOS == "windows" {
				// Windows cannot rename over an existing destination.
				_ = os.Remove(local)
			}
			if err := os.Rename(tmpPath, local); err != nil {
				return err
			}
			writeEvent(d, session, "file_operation", map[string]any{
				"op": "download", "remote": remote, "local": local,
			})
			fmt.Fprintf(d.Stderr, "[colab] Downloaded '%s' to '%s'\n", remote, local)
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	cmd.Flags().BoolVarP(&recursive, "recursive", "r", false, "Recursively download a directory")
	return cmd
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func editorCommand() string {
	if v := strings.TrimSpace(os.Getenv("VISUAL")); v != "" {
		return v
	}
	if v := strings.TrimSpace(os.Getenv("EDITOR")); v != "" {
		return v
	}
	switch runtime.GOOS {
	case "windows":
		return "notepad"
	default:
		if platform.IsTermux() {
			return "nano"
		}
		return "vi"
	}
}

func editCommand() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "edit REMOTE",
		Short: "Edit a file on a running Colab session",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			d := depsOf(cmd)
			if err := validateSessionName(name); err != nil {
				return err
			}
			remote := args[0]
			c, session, err := contentsForSession(cmd.Context(), d, name)
			if err != nil {
				return err
			}
			dir, err := os.MkdirTemp(platform.TempDir(), "colab-edit-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(dir) }()
			local := filepath.Join(dir, "file"+filepath.Ext(remote))
			f, err := os.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, err = c.ReadStream(cmd.Context(), remote, f)
			if closeErr := f.Close(); closeErr != nil && err == nil {
				err = closeErr
			}
			if err != nil && !errors.Is(err, contents.ErrNotFound) {
				// Δ12: unlike upstream, a non-404 error must never lead to an
				// empty buffer being uploaded over the real remote file.
				return exitf(1, "[colab] Download failed: %s", err)
			}
			before, err := fileSHA256(local)
			if err != nil {
				return err
			}
			parts, err := shellquote.Split(editorCommand())
			if err != nil || len(parts) == 0 {
				return usagef("[colab] Invalid editor command: %s", editorCommand())
			}
			parts = append(parts, local)
			editor := exec.CommandContext(cmd.Context(), parts[0], parts[1:]...)
			editor.Stdin, editor.Stdout, editor.Stderr = d.Stdin, d.Stdout, d.Stderr
			if err := editor.Run(); err != nil {
				return exitf(1, "[colab] Editor failed: %s", err)
			}
			after, err := fileSHA256(local)
			if err != nil {
				return err
			}
			if after == before {
				fmt.Fprintf(d.Stderr, "[colab] No changes made to '%s'\n", remote)
				return nil
			}
			src, err := os.Open(local)
			if err != nil {
				return err
			}
			defer func() { _ = src.Close() }()
			if err := c.WriteStream(cmd.Context(), remote, src, contents.DefaultChunkSize); err != nil {
				return exitf(1, "[colab] Upload failed: %s", err)
			}
			writeEvent(d, session, "file_operation", map[string]any{"op": "edit", "remote": remote})
			fmt.Fprintf(d.Stderr, "[colab] Edited and uploaded '%s'\n", remote)
			return nil
		},
	}
	cmd.Flags().StringVarP(&name, "session", "s", "", "Session name")
	return cmd
}

func registerFileCommands(root *cobra.Command) {
	root.AddCommand(lsCommand())
	root.AddCommand(rmCommand())
	root.AddCommand(uploadCommand())
	root.AddCommand(downloadCommand())
	root.AddCommand(editCommand())
}
