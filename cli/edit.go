// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/daniel-alexander4/vigo/server/validate"
	"github.com/spf13/cobra"
)

// vigoConfigRoot is the base directory for .vgo config files.
// vi only allows editing files within this tree.
const vigoConfigRoot = "/srv/vigo/stack"

var viCmd = &cobra.Command{
	Use:   "vi <file.vgo>",
	Short: "Open a .vgo config file in an editor with YAML validation on save",
	Long: `Opens a .vgo file in vim with vigo-aware syntax highlighting.

Files must be within /srv/vigo/stack/. Paths can be relative —
if the file is not found at the given path, it is resolved relative
to /srv/vigo/stack/stage/.

On save, the file is checked for YAML correctness and idempotency. If
validation fails, the write is aborted and errors are shown. Use :w! to
force-save past validation.

Use 'vigogo vi' to open files with validation.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path, err := resolveVigoPath(args[0])
		if err != nil {
			return err
		}
		return runEditor(path)
	},
}

// resolveVigoPath resolves a file path within the stack config directory.
// Accepts absolute paths (must be within /srv/vigo/stack/) or relative
// paths (resolved against /srv/vigo/stack/stage/).
func resolveVigoPath(input string) (string, error) {
	var absPath string
	if filepath.IsAbs(input) {
		absPath = filepath.Clean(input)
	} else {
		// Try the input as-is first (relative to cwd).
		if _, err := os.Stat(input); err == nil {
			absPath, _ = filepath.Abs(input)
		} else {
			// Resolve relative to stage/ directory.
			absPath = filepath.Join(vigoConfigRoot, "stage", input)
		}
	}

	// Resolve symlinks to get the real path.
	realPath, err := filepath.EvalSymlinks(filepath.Dir(absPath))
	if err != nil {
		// Directory doesn't exist yet — use the cleaned path.
		realPath = filepath.Dir(absPath)
	}
	absPath = filepath.Join(realPath, filepath.Base(absPath))

	// Ensure the resolved path is within the allowed root.
	relPath, err := filepath.Rel(vigoConfigRoot, absPath)
	if err != nil || relPath == ".." || len(relPath) > 2 && relPath[:3] == "../" {
		return "", fmt.Errorf("file must be within %s (got %s)", vigoConfigRoot, absPath)
	}

	return absPath, nil
}

func init() {
}

func runEditor(absPath string) error {
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

	// Resolve the vigogo binary for validation callbacks.
	cliPath, err := os.Executable()
	if err != nil {
		cliPath = "vigogo"
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

	// Post-save validation (belt-and-suspenders in case BufWritePost didn't catch it).
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

// vigoSyntax is a vim syntax file for .vgo files. It extends YAML with
// vigo-specific keyword highlighting.
const vigoSyntax = `" Vim syntax file for Vigo .vgo configuration files
" Language: Vigo Configuration (YAML-based)

" Start with YAML as the base syntax
runtime! syntax/yaml.vim

" Vigo top-level keys
syn match vigoTopKey    /^\s*\(name\|vars\|resources\|modules\|roles\|envoys\|depends_on\|when\|environment\|environment_overrides\|match\|conditional_vars\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi vigoTopKey  ctermfg=33  guifg=#0087ff cterm=bold gui=bold

" Resource attributes
syn match vigoResKey    /^\s*\(type\|state\|targetpath\|content\|source\|mode\|owner\|group\|groups\|package\|service\|enabled\|command\|creates\|onlyif\|unless\|cwd\|user\|username\|authorized_keys\|notify\|subscribes\|depends_on\|baseindent\|job\|schedule\|minute\|hour\|day\|month\|weekday\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi vigoResKey  ctermfg=33  guifg=#0087ff

" Resource type values
syn match vigoResType   /\<\(file\|package\|service\|user\|exec\|repository\|archive\|cron\)\>/
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

// vigoVimrc is the custom vimrc template. %s placeholders:
// 1: tmpDir (runtimepath), 2: vigogo path
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
  let l:output = system('%s validate ' . shellescape(l:tmpfile))
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
