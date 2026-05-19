import { z } from "zod";

export const loginSchema = z.object({
  email: z.string().min(1, "请输入邮箱").email("邮箱格式不正确"),
  password: z.string().min(1, "请输入密码"),
});

export const registerSchema = z.object({
  display_name: z.string().optional(),
  email: z.string().min(1, "请输入邮箱").email("邮箱格式不正确"),
  password: z.string().min(8, "密码至少需要 8 位"),
  verification_code: z.string().optional(),
});

export const resetRequestSchema = z.object({
  email: z.string().min(1, "请输入邮箱").email("邮箱格式不正确"),
});

export const resetConfirmSchema = z.object({
  token: z.string().min(1, "请输入重置令牌"),
  password: z.string().min(8, "密码至少需要 8 位"),
});

export type LoginValues = z.infer<typeof loginSchema>;
export type RegisterValues = z.infer<typeof registerSchema>;
export type ResetRequestValues = z.infer<typeof resetRequestSchema>;
export type ResetConfirmValues = z.infer<typeof resetConfirmSchema>;
