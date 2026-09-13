package roleprofile

func readOnlyTools() []string {
	return []string{
		"read_file", "list_files", "search_code",
		"git_status", "git_diff", "git_log", "git_branches", "git_show_commit",
		"read_tool_result", "go_to_definition", "find_references", "diagnostics", "document_symbols",
	}
}

func implementationTools() []string {
	return []string{
		"read_file", "list_files", "search_code",
		"git_status", "git_diff", "git_log", "git_branches", "git_show_commit",
		"apply_patch", "create_file", "edit_file", "replace_file",
		"list_check_plans", "run_checks", "read_tool_result",
		"go_to_definition", "find_references", "diagnostics", "document_symbols",
	}
}
