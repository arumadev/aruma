import type { Timestamp } from 'firebase/firestore';

// The shared data model of Team Board (the web and Flutter examples use the same one). A web
// client and a Flutter client of one project see each other's teams, tasks, chat and presence.
//
// Firestore
//   users/{uid}                     Profile
//   teams/{teamId}                  Team (teamId is the join code)
//   teams/{teamId}/members/{uid}    Member
//   teams/{teamId}/tasks/{taskId}   Task
// Realtime Database
//   members/{teamId}/{uid}          true, mirrors the Firestore membership for the RTDB rules
//   chat/{teamId}/messages/{id}     ChatMessage
//   presence/{teamId}/{uid}         Presence
// Storage
//   avatars/{uid}/avatar                       the profile picture (an image under 2 MB)
//   teams/{teamId}/tasks/{taskId}/{filename}   a task's attachment (under 10 MB), members only

export interface Profile {
  name: string;
  photoURL: string | null;
  updatedAt?: Timestamp | null;
}

export interface Team {
  id: string;
  name: string;
  owner: string;
  createdAt: Timestamp | null;
}

export type Role = 'owner' | 'member';

export interface Member {
  uid: string;
  role: Role;
  name: string;
  joinedAt: Timestamp | null;
}

export interface Attachment {
  name: string;
  path: string;
}

export interface Task {
  id: string;
  title: string;
  done: boolean;
  assignee: string | null;
  createdBy: string;
  createdAt: Timestamp | null;
  updatedAt: Timestamp | null;
  attachment: Attachment | null;
}

export interface ChatMessage {
  id: string;
  uid: string;
  name: string;
  text: string;
  ts: number;
}

export interface Presence {
  uid: string;
  name: string;
  online: boolean;
  lastSeen: number;
}

export const limits = {
  name: 40,
  teamName: 60,
  taskTitle: 200,
  message: 1000,
  avatarBytes: 2 * 1024 * 1024,
  attachmentBytes: 10 * 1024 * 1024,
} as const;
