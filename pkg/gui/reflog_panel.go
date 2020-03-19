package gui

import (
	"regexp"

	"github.com/jesseduffield/gocui"
	"github.com/jesseduffield/lazygit/pkg/commands"
	"github.com/jesseduffield/lazygit/pkg/gui/presentation"
)

// list panel functions

func (gui *Gui) getSelectedReflogCommit() *commands.Commit {
	selectedLine := gui.State.Panels.ReflogCommits.SelectedLine
	if selectedLine == -1 || len(gui.State.ReflogCommits) == 0 {
		return nil
	}

	return gui.State.ReflogCommits[selectedLine]
}

func (gui *Gui) handleReflogCommitSelect(g *gocui.Gui, v *gocui.View) error {
	if gui.popupPanelFocused() {
		return nil
	}

	gui.State.SplitMainPanel = false

	if _, err := gui.g.SetCurrentView(v.Name()); err != nil {
		return err
	}

	gui.getMainView().Title = "Reflog Entry"

	commit := gui.getSelectedReflogCommit()
	if commit == nil {
		return gui.newStringTask("main", "No reflog history")
	}
	v.FocusPoint(0, gui.State.Panels.ReflogCommits.SelectedLine)

	cmd := gui.OSCommand.ExecutableFromString(
		gui.GitCommand.ShowCmdStr(commit.Sha),
	)
	if err := gui.newPtyTask("main", cmd); err != nil {
		gui.Log.Error(err)
	}

	return nil
}

func (gui *Gui) refreshReflogCommits() error {
	previousLength := len(gui.State.ReflogCommits)
	commits, err := gui.GitCommand.GetReflogCommits()
	if err != nil {
		return gui.createErrorPanel(gui.g, err.Error())
	}

	if len(commits) > previousLength {
		if gui.State.Undoing {
			gui.State.UndoReflogIdx += len(commits) - previousLength
		} else {
			gui.State.UndoReflogIdx = 0
		}
	}

	gui.State.ReflogCommits = commits

	if gui.getCommitsView().Context == "reflog-commits" {
		return gui.renderReflogCommitsWithSelection()
	}

	return nil
}

func (gui *Gui) renderReflogCommitsWithSelection() error {
	commitsView := gui.getCommitsView()

	gui.refreshSelectedLine(&gui.State.Panels.ReflogCommits.SelectedLine, len(gui.State.ReflogCommits))
	displayStrings := presentation.GetCommitListDisplayStrings(gui.State.ReflogCommits, gui.State.ScreenMode != SCREEN_NORMAL)
	gui.renderDisplayStrings(commitsView, displayStrings)
	if gui.g.CurrentView() == commitsView && commitsView.Context == "reflog-commits" {
		if err := gui.handleReflogCommitSelect(gui.g, commitsView); err != nil {
			return err
		}
	}

	return nil
}

func (gui *Gui) handleCheckoutReflogCommit(g *gocui.Gui, v *gocui.View) error {
	commit := gui.getSelectedReflogCommit()
	if commit == nil {
		return nil
	}

	err := gui.createConfirmationPanel(g, gui.getCommitsView(), true, gui.Tr.SLocalize("checkoutCommit"), gui.Tr.SLocalize("SureCheckoutThisCommit"), func(g *gocui.Gui, v *gocui.View) error {
		return gui.handleCheckoutRef(commit.Sha)
	}, nil)
	if err != nil {
		return err
	}

	gui.State.Panels.ReflogCommits.SelectedLine = 0

	return nil
}

func (gui *Gui) handleCreateReflogResetMenu(g *gocui.Gui, v *gocui.View) error {
	commit := gui.getSelectedReflogCommit()

	return gui.createResetMenu(commit.Sha)
}

type reflogAction struct {
	regexStr string
	action   func(match []string, commitSha string, prevCommitSha string) (bool, error)
}

func (gui *Gui) reflogUndo(g *gocui.Gui, v *gocui.View) error {
	reflogActions := []reflogAction{
		{
			regexStr: `^checkout: moving from ([\S]+)`,
			action: func(match []string, commitSha string, prevCommitSha string) (bool, error) {
				if len(match) <= 1 {
					return false, nil
				}
				gui.State.Undoing = true
				defer func() { gui.State.Undoing = false }()

				return true, gui.handleCheckoutRef(match[1])
			},
		},
		{
			regexStr: `^commit|^rebase -i \(start\)`,
			action: func(match []string, commitSha string, prevCommitSha string) (bool, error) {
				return true, gui.handleHardResetWithAutoStash(prevCommitSha)
			},
		},
	}

	for i, reflogCommit := range gui.State.ReflogCommits[gui.State.UndoReflogIdx:] {
		for _, action := range reflogActions {
			re := regexp.MustCompile(action.regexStr)
			match := re.FindStringSubmatch(reflogCommit.Name)
			if len(match) == 0 {
				continue
			}
			prevCommitSha := ""
			if len(gui.State.ReflogCommits)-1 >= i+1 {
				prevCommitSha = gui.State.ReflogCommits[i+1].Sha
			}

			done, err := action.action(match, reflogCommit.Sha, prevCommitSha)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	}

	return nil
}

// only to be used in the undo flow for now
func (gui *Gui) handleHardResetWithAutoStash(commitSha string) error {
	// if we have any modified tracked files we need to ask the user if they want us to stash for them
	dirtyWorkingTree := false
	for _, file := range gui.State.Files {
		if file.Tracked {
			dirtyWorkingTree = true
			break
		}
	}

	gui.State.Undoing = true
	defer func() { gui.State.Undoing = false }()

	if dirtyWorkingTree {
		// offer to autostash changes
		return gui.createConfirmationPanel(gui.g, gui.getBranchesView(), true, gui.Tr.SLocalize("AutoStashTitle"), gui.Tr.SLocalize("AutoStashPrompt"), func(g *gocui.Gui, v *gocui.View) error {
			if err := gui.GitCommand.StashSave(gui.Tr.SLocalize("StashPrefix") + commitSha); err != nil {
				return gui.createErrorPanel(g, err.Error())
			}
			if err := gui.resetToRef(commitSha, "hard"); err != nil {
				return gui.createErrorPanel(g, err.Error())
			}

			if err := gui.GitCommand.StashDo(0, "pop"); err != nil {
				if err := gui.refreshSidePanels(g); err != nil {
					return err
				}
				return gui.createErrorPanel(g, err.Error())
			}
			return gui.refreshSidePanels(g)
		}, nil)
	}

	if err := gui.resetToRef(commitSha, "hard"); err != nil {
		return gui.createErrorPanel(gui.g, err.Error())
	}
	return gui.refreshSidePanels(gui.g)
}
