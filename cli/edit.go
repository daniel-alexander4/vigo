// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/daniel-alexander4/vigo/server/validate"
	"github.com/spf13/cobra"
)

// vigoConfigRoot is the base directory for .vgo config files.
// edit only allows editing files within this tree.
const vigoConfigRoot = "/srv/vigo"

var configEditCmd = &cobra.Command{
	Use:   "edit <name-or-path>",
	Short: "Open a .vgo config file in an editor with YAML validation on save",
	Long: `Opens a .vgo file in the specified editor with validation.

Accepts a module name, a relative path, or an absolute path. Module names
are searched recursively in stockpile/modules/ (e.g., "nginx" finds
stockpile/modules/system/nginx.vgo). The .vgo extension is optional.

Editor defaults to $EDITOR, falling back to vi. Override with --editor.
vi/vim get vigo-aware syntax highlighting and write-time validation.
Other editors get post-save validation only.

Examples:
  vigocli config edit nginx
  vigocli config edit ssh-hardening --editor nano
  vigocli config edit modules/nginx.vgo
  vigocli config edit alexander4/us-west1/us-west1-a/danlap.vgo`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		editor, _ := cmd.Flags().GetString("editor")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			editor = "vi"
		}

		path, err := resolveVigoPath(args[0])
		if err != nil {
			// Not found as a path — try as a module name.
			found, findErr := findModuleByName(args[0])
			if findErr != nil {
				return findErr
			}
			path = found
		}
		return runEditorWithName(editor, path)
	},
}

// findModuleByName searches the entire stockpile/ tree recursively for a
// .vgo file by name. Accepts "nginx", "nginx.vgo", or "danlap" — returns
// the absolute path.
func findModuleByName(name string) (string, error) {
	name = filepath.Base(name)
	if !strings.HasSuffix(name, ".vgo") {
		name += ".vgo"
	}

	stockpileDir := filepath.Join(vigoConfigRoot, "stockpile")

	var matches []string
	_ = filepath.WalkDir(stockpileDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Name() == name {
			matches = append(matches, path)
		}
		return nil
	})

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("%q not found in stockpile", strings.TrimSuffix(name, ".vgo"))
	case 1:
		return matches[0], nil
	default:
		rel := make([]string, len(matches))
		for i, m := range matches {
			r, _ := filepath.Rel(vigoConfigRoot, m)
			rel[i] = "  " + r
		}
		return "", fmt.Errorf("multiple matches for %q:\n%s\nSpecify the full path", strings.TrimSuffix(name, ".vgo"), strings.Join(rel, "\n"))
	}
}

// resolveVigoPath resolves a file path within the stack config directory.
// Returns an error if the resolved file does not exist.
func resolveVigoPath(input string) (string, error) {
	var absPath string
	if filepath.IsAbs(input) {
		absPath = filepath.Clean(input)
	} else {
		// Try the input as-is first (relative to cwd).
		if _, err := os.Stat(input); err == nil {
			absPath, _ = filepath.Abs(input)
		} else {
			// Resolve relative to stockpile/ directory.
			absPath = filepath.Join(vigoConfigRoot, "stockpile", input)
		}
	}

	// Resolve symlinks to get the real path.
	realPath, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		realPath = filepath.Dir(absPath)
	}
	absPath = filepath.Join(realPath, filepath.Base(absPath))

	// Ensure the resolved path is within the allowed root.
	relPath, err := filepath.Rel(vigoConfigRoot, absPath)
	if err != nil || relPath == ".." || len(relPath) > 2 && relPath[:3] == "../" {
		return "", fmt.Errorf("file must be within %s (got %s)", vigoConfigRoot, absPath)
	}

	// Verify the file exists — if not, fall through to name-based search.
	if _, err := os.Stat(absPath); err != nil {
		return "", err
	}

	return absPath, nil
}

func runEditorWithName(editor, absPath string) error {
	// vi and vim get the full vigo experience (syntax + validation on write).
	if editor == "vi" || editor == "vim" {
		return runVimEditor(absPath)
	}

	// All other editors: open directly, validate after save.
	cmd := exec.Command(editor, absPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s exited with error: %w", editor, err)
	}

	// Post-save validation.
	findings, err := validateFile(absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: post-edit validation failed: %v\n", err)
		return nil
	}
	if validate.HasErrors(findings) {
		fmt.Fprintln(os.Stderr, "validation errors:")
		for _, f := range findings {
			fmt.Fprintln(os.Stderr, " ", f.String())
		}
	} else {
		fmt.Println("validation passed")
	}

	return nil
}

func runVimEditor(absPath string) error {
	// Create temp directory for vim config files.
	tmpDir, err := os.MkdirTemp("", "vigovi-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write syntax file.
	syntaxDir := filepath.Join(tmpDir, "syntax")
	if err := os.MkdirAll(syntaxDir, 0755); err != nil {
		return fmt.Errorf("creating syntax dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(syntaxDir, "vigo.vim"), []byte(vigoSyntax), 0600); err != nil {
		return fmt.Errorf("writing syntax file: %w", err)
	}

	// Write ftdetect file.
	ftdetectDir := filepath.Join(tmpDir, "ftdetect")
	if err := os.MkdirAll(ftdetectDir, 0755); err != nil {
		return fmt.Errorf("creating ftdetect dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(ftdetectDir, "vigo.vim"), []byte(vigoFtdetect), 0600); err != nil {
		return fmt.Errorf("writing ftdetect file: %w", err)
	}

	// Resolve the vigocli binary for validation callbacks.
	cliPath, err := os.Executable()
	if err != nil {
		cliPath = "vigocli"
	}

	// Build vimrc.
	vimrc := fmt.Sprintf(vigoVimrc, tmpDir, cliPath)
	vimrcPath := filepath.Join(tmpDir, "vimrc")
	if err := os.WriteFile(vimrcPath, []byte(vimrc), 0600); err != nil {
		return fmt.Errorf("writing vimrc: %w", err)
	}

	// Launch vim.
	cmd := exec.Command("vim", "-u", vimrcPath, absPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("vim exited with error: %w", err)
	}

	// Post-save validation.
	findings, err := validateFile(absPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: post-edit validation failed: %v\n", err)
		return nil
	}
	if validate.HasErrors(findings) {
		fmt.Fprintln(os.Stderr, "warning: file has validation errors:")
		for _, f := range findings {
			fmt.Fprintln(os.Stderr, " ", f.String())
		}
	}

	return nil
}

func init() {
	configEditCmd.Flags().String("editor", "", "Editor to use (default: $EDITOR or vi)")
	configCmd.AddCommand(configEditCmd)
}

// vigoSyntax is a vim syntax file for .vgo files.
const vigoSyntax = `" Vim syntax file for Vigo .vgo configuration files
" Language: Vigo Configuration (YAML-based)

" Start with YAML as the base syntax
runtime! syntax/yaml.vim

" Vigo top-level keys
syn match vigoTopKey    /^\s*\(name\|vars\|resources\|modules\|roles\|envoys\|depends_on\|when\|environment\|env\|match\|conditional_vars\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi vigoTopKey  ctermfg=33  guifg=#0087ff cterm=bold gui=bold

" Resource attributes
syn match vigoResKey    /^\s*\(type\|state\|targetpath\|content\|source\|mode\|owner\|group\|groups\|package\|service\|enabled\|command\|creates\|onlyif\|unless\|cwd\|user\|username\|authorized_keys\|notify\|subscribes\|depends_on\|baseindent\|job\|schedule\|minute\|hour\|day\|month\|weekday\|backup_dir\|retain\|exclude\|cross_fs\|min_free_space\|pre_command\|post_command\|db_type\|db_name\|db_user\|db_host\|db_port\|db_dump_command\|desktop\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi vigoResKey  ctermfg=33  guifg=#0087ff

" Resource type values
syn match vigoResType   /\<\(file\|package\|service\|user\|exec\|repository\|archive\|cron\|backup\|db_backup\|directory\|symlink\)\>/
hi vigoResType ctermfg=214 guifg=#ffaf00 cterm=bold gui=bold

" State values
syn match vigoState     /\<\(present\|absent\|running\|stopped\|restarted\|reloaded\|directory\)\>/
hi vigoState   ctermfg=40  guifg=#00d700

" Secret references
syn match vigoSecret    /secret:[^ \t]*/
hi vigoSecret  ctermfg=196 guifg=#ff0000 cterm=bold gui=bold

" Go template expressions
syn region vigoTemplate start=/{{/ end=/}}/ containedin=yamlFlowString,yamlString
hi vigoTemplate ctermfg=171 guifg=#d75fff

" Idempotency guard keywords (highlighted to draw attention)
syn match vigoGuard     /^\s*\(creates\|onlyif\|unless\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi vigoGuard   ctermfg=208 guifg=#ff8700 cterm=bold gui=bold

" Environment names
syn match vigoEnv       /\<\(production\|staging\|development\|testing\)\>/
hi vigoEnv     ctermfg=75  guifg=#5fafff

" Match patterns (quoted string after match:)
syn match vigoMatch     /\(match:\s*\)\@<="[^"]*"/ containedin=yamlFlowString,yamlString
syn match vigoMatch     /\(match:\s*\)\@<=\S\+/ containedin=yamlPlainScalar
hi vigoMatch   ctermfg=15  guifg=#ffffff cterm=bold gui=bold

" Comments
hi! yamlComment ctermfg=34 guifg=#00af00
`

// vigoFtdetect sets up filetype detection for .vgo files.
const vigoFtdetect = `au BufRead,BufNewFile *.vgo set filetype=vigo`

// vigoVimrc is the custom vimrc template.
const vigoVimrc = `" Vigo editor vimrc — auto-generated, do not edit
set nocompatible
set runtimepath+=%s
filetype plugin on
syntax on
set number
set expandtab
set tabstop=2
set shiftwidth=2
set softtabstop=2
set autoindent

" Validate on write
function! VigoValidate()
  let l:tmpfile = tempname() . '.vgo'
  call writefile(getline(1, '$'), l:tmpfile)
  let l:output = system('%s config validate ' . shellescape(l:tmpfile))
  let l:exit_code = v:shell_error
  call delete(l:tmpfile)
  if l:exit_code != 0
    echohl ErrorMsg
    for l:line in split(l:output, '\n')
      echomsg l:line
    endfor
    echohl None
    echomsg 'Validation failed — write aborted. Use :w! to force.'
    throw 'vigo:validation_failed'
  endif
endfunction

augroup vigo_validate
  autocmd!
  autocmd BufWritePre *.vgo call VigoValidate()
augroup END

" Status line shows file type
set laststatus=2
set statusline=%%f\ [vigo]\ %%m%%=%%l:%%c\ %%P
`
