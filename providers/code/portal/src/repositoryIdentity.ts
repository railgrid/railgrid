// Shared by form validation and API submission. Dot segments are not repository names.
export const REMOTE_REPOSITORY_PATTERN = /^[A-Za-z0-9-]+\/(?!\.{1,2}$)[A-Za-z0-9_.-]+$/

export const REPOSITORY_NAME_PATTERN = /^(?!\.{1,2}$)[A-Za-z0-9_.-]{1,100}$/
