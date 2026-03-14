// Copyright (c) 2026 Alexander4, LLC
// SPDX-License-Identifier: LicenseRef-Proprietary

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hobnob/hobnob/server/validate"
	"github.com/spf13/cobra"
)

var viCmd = &cobra.Command{
	Use:   "vi <file.hnb>",
	Short: "Edit a .hnb file with syntax highlighting and validation",
	Long: `Opens a .hnb file in vim with hobnob-aware syntax highlighting.

On save, the file is checked for YAML correctness and idempotency. If
validation fails, the write is aborted and errors are shown. Use :w! to
force-save past validation.

Can also be invoked as 'hnbvi' for convenience.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEditor(args[0])
	},
}

func init() {
}

func runEditor(filePath string) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	// Create temp directory for vim config files.
	tmpDir, err := os.MkdirTemp("", "hnbvi-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write syntax file.
	syntaxDir := filepath.Join(tmpDir, "syntax")
	if err := os.MkdirAll(syntaxDir, 0755); err != nil {
		return fmt.Errorf("creating syntax dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(syntaxDir, "hnb.vim"), []byte(hobSyntax), 0600); err != nil {
		return fmt.Errorf("writing syntax file: %w", err)
	}

	// Write ftdetect file.
	ftdetectDir := filepath.Join(tmpDir, "ftdetect")
	if err := os.MkdirAll(ftdetectDir, 0755); err != nil {
		return fmt.Errorf("creating ftdetect dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(ftdetectDir, "hnb.vim"), []byte(hobFtdetect), 0600); err != nil {
		return fmt.Errorf("writing ftdetect file: %w", err)
	}

	// Resolve the hobnobcli binary for validation callbacks.
	cliPath, err := os.Executable()
	if err != nil {
		cliPath = "hobnobcli"
	}

	// Build vimrc.
	vimrc := fmt.Sprintf(hobVimrc, tmpDir, cliPath)
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

// hobSyntax is a vim syntax file for .hnb files. It extends YAML with
// hobnob-specific keyword highlighting.
const hobSyntax = `" Vim syntax file for Hobnob .hnb configuration files
" Language: Hobnob Configuration (YAML-based)

" Start with YAML as the base syntax
runtime! syntax/yaml.vim

" Hobnob top-level keys
syn match hobTopKey    /^\s*\(name\|vars\|resources\|modules\|roles\|thralls\|depends_on\|when\|environment\|environment_overrides\|match\|conditional_vars\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi hobTopKey  ctermfg=33  guifg=#0087ff cterm=bold gui=bold

" Resource attributes
syn match hobResKey    /^\s*\(type\|state\|targetpath\|content\|source\|mode\|owner\|group\|groups\|package\|service\|enabled\|command\|creates\|onlyif\|unless\|cwd\|user\|username\|authorized_keys\|notify\|subscribes\|depends_on\|baseindent\|job\|schedule\|minute\|hour\|day\|month\|weekday\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi hobResKey  ctermfg=33  guifg=#0087ff

" Resource type values
syn match hobResType   /\<\(file\|package\|service\|user\|exec\|repository\|archive\|cron\)\>/
hi hobResType ctermfg=214 guifg=#ffaf00 cterm=bold gui=bold

" State values
syn match hobState     /\<\(present\|absent\|running\|stopped\|restarted\|reloaded\|directory\)\>/
hi hobState   ctermfg=40  guifg=#00d700

" Secret references
syn match hobSecret    /secret:[^ \t]*/
hi hobSecret  ctermfg=196 guifg=#ff0000 cterm=bold gui=bold

" Go template expressions
syn region hobTemplate start=/{{/ end=/}}/ containedin=yamlFlowString,yamlString
hi hobTemplate ctermfg=171 guifg=#d75fff

" Idempotency guard keywords (highlighted to draw attention)
syn match hobGuard     /^\s*\(creates\|onlyif\|unless\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi hobGuard   ctermfg=208 guifg=#ff8700 cterm=bold gui=bold

" Environment names
syn match hobEnv       /\<\(production\|staging\|development\|testing\)\>/
hi hobEnv     ctermfg=75  guifg=#5fafff

" Match patterns (quoted string after match:)
syn match hobMatch     /\(match:\s*\)\@<="[^"]*"/ containedin=yamlFlowString,yamlString
syn match hobMatch     /\(match:\s*\)\@<=\S\+/ containedin=yamlPlainScalar
hi hobMatch   ctermfg=15  guifg=#ffffff cterm=bold gui=bold

" Comments
hi! yamlComment ctermfg=34 guifg=#00af00
`

// hobFtdetect sets up filetype detection for .hnb files.
const hobFtdetect = `au BufRead,BufNewFile *.hnb set filetype=hnb`

// hobVimrc is the custom vimrc template. %s placeholders:
// 1: tmpDir (runtimepath), 2: hobnobcli path
const hobVimrc = `" Hobnob editor vimrc — auto-generated, do not edit
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
function! HobValidate()
  let l:tmpfile = tempname() . '.hnb'
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
    throw 'hobnob:validation_failed'
  endif
endfunction

augroup hobnob_validate
  autocmd!
  autocmd BufWritePre *.hnb call HobValidate()
augroup END

" Status line shows file type
set laststatus=2
set statusline=%%f\ [hnb]\ %%m%%=%%l:%%c\ %%P
`
