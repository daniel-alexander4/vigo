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
	Use:   "vi <file.hob>",
	Short: "Edit a .hob file with syntax highlighting and validation",
	Long: `Opens a .hob file in vim with hobnob-aware syntax highlighting.

On save, the file is checked for YAML correctness and idempotency. If
validation fails, the write is aborted and errors are shown. Use --dangerous
to skip validation on save.

Can also be invoked as 'hobvi' for convenience.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dangerous, _ := cmd.Flags().GetBool("dangerous")
		return runEditor(args[0], dangerous)
	},
}

func init() {
	viCmd.Flags().Bool("dangerous", false, "Allow saving non-idempotent resources")
}

func runEditor(filePath string, dangerous bool) error {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return fmt.Errorf("resolving path: %w", err)
	}

	// Create temp directory for vim config files.
	tmpDir, err := os.MkdirTemp("", "hobvi-*")
	if err != nil {
		return fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write syntax file.
	syntaxDir := filepath.Join(tmpDir, "syntax")
	if err := os.MkdirAll(syntaxDir, 0755); err != nil {
		return fmt.Errorf("creating syntax dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(syntaxDir, "hob.vim"), []byte(hobSyntax), 0644); err != nil {
		return fmt.Errorf("writing syntax file: %w", err)
	}

	// Write ftdetect file.
	ftdetectDir := filepath.Join(tmpDir, "ftdetect")
	if err := os.MkdirAll(ftdetectDir, 0755); err != nil {
		return fmt.Errorf("creating ftdetect dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(ftdetectDir, "hob.vim"), []byte(hobFtdetect), 0644); err != nil {
		return fmt.Errorf("writing ftdetect file: %w", err)
	}

	// Resolve the hobcli binary for validation callbacks.
	hobcliPath, err := os.Executable()
	if err != nil {
		hobcliPath = "hobcli"
	}

	// Build vimrc.
	dangerousFlag := ""
	if dangerous {
		dangerousFlag = " --dangerous"
	}
	vimrc := fmt.Sprintf(hobVimrc, tmpDir, hobcliPath, dangerousFlag)
	vimrcPath := filepath.Join(tmpDir, "vimrc")
	if err := os.WriteFile(vimrcPath, []byte(vimrc), 0644); err != nil {
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
	if !dangerous {
		findings, err := validateFile(absPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: post-edit validation failed: %v\n", err)
			return nil
		}
		if validate.HasErrors(findings) {
			fmt.Fprintln(os.Stderr, "warning: file has idempotency errors:")
			for _, f := range findings {
				fmt.Fprintln(os.Stderr, " ", f.String())
			}
		}
	}

	return nil
}

// hobSyntax is a vim syntax file for .hob files. It extends YAML with
// hobnob-specific keyword highlighting.
const hobSyntax = `" Vim syntax file for Hobnob .hob configuration files
" Language: Hobnob Configuration (YAML-based)

" Start with YAML as the base syntax
runtime! syntax/yaml.vim

" Hobnob top-level keys
syn match hobTopKey    /^\s*\(name\|vars\|resources\|modules\|roles\|clients\|depends_on\|when\|environment\|env_vars\|match\|conditional_vars\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi def link hobTopKey  Keyword

" Resource attributes
syn match hobResKey    /^\s*\(type\|state\|targetpath\|content\|source\|mode\|owner\|group\|package\|service\|enabled\|command\|creates\|onlyif\|unless\|cwd\|user\|username\|notify\|subscribes\|depends_on\|baseindent\|job\|schedule\|minute\|hour\|day\|month\|weekday\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi def link hobResKey  Type

" Resource type values
syn match hobResType   /\<\(file\|package\|service\|user\|exec\|repository\|archive\|cron\)\>/
hi def link hobResType Constant

" State values
syn match hobState     /\<\(present\|absent\|running\|stopped\|restarted\|reloaded\|directory\)\>/
hi def link hobState   Boolean

" Secret references
syn match hobSecret    /secret:[^ \t]*/
hi def link hobSecret  WarningMsg

" Go template expressions
syn region hobTemplate start=/{{/ end=/}}/ containedin=yamlFlowString,yamlString
hi def link hobTemplate PreProc

" Idempotency guard keywords (highlighted to draw attention)
syn match hobGuard     /^\s*\(creates\|onlyif\|unless\)\s*:/he=e-1 contained containedin=yamlBlockMappingKey
hi def link hobGuard   Special
`

// hobFtdetect sets up filetype detection for .hob files.
const hobFtdetect = `au BufRead,BufNewFile *.hob set filetype=hob`

// hobVimrc is the custom vimrc template. %s placeholders:
// 1: tmpDir (runtimepath), 2: hobcli path, 3: --dangerous flag or ""
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
  let l:output = system('%s validate%s ' . shellescape(expand('%%:p')))
  if v:shell_error != 0
    echohl ErrorMsg
    for l:line in split(l:output, '\n')
      echomsg l:line
    endfor
    echohl None
    echomsg 'Idempotency check failed — write aborted. Use :w! to force or --dangerous to disable.'
    throw 'hobnob:validation_failed'
  endif
endfunction

augroup hobnob_validate
  autocmd!
  autocmd BufWritePre *.hob call HobValidate()
augroup END

" Status line shows file type
set laststatus=2
set statusline=%%f\ [hob]\ %%m%%=%%l:%%c\ %%P
`
